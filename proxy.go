package main

import (
	// 标准库
	"bufio"
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	// 本地包
	"mediaProxy/base"

	// 第三方库
	"github.com/go-resty/resty/v2"
	"github.com/patrickmn/go-cache"
	log "github.com/sirupsen/logrus"
)

//go:embed static/index.html
var indexHTML embed.FS

var mediaCache = cache.New(4*time.Hour, 10*time.Minute)

type Chunk struct {
	startOffset int64
	endOffset   int64
	buffer      []byte
}

func newChunk(start int64, end int64) *Chunk {
	return &Chunk{
		startOffset: start,
		endOffset:   end,
	}
}

func (ch *Chunk) get() []byte {
	return ch.buffer
}

func (ch *Chunk) put(buffer []byte) {
	ch.buffer = buffer
}

type ProxyDownloadStruct struct {
	ProxyRunning         bool
	NextChunkStartOffset int64
	CurrentOffset        int64
	CurrentChunk         int64
	ChunkSize            int64
	MaxBufferedChunk     int64
	EndOffset            int64
	ProxyMutex           *sync.Mutex
	ProxyTimeout         int64
	ReadyChunkQueue      chan *Chunk
	ThreadCount          int64
	DownloadUrl          string
}

func newProxyDownloadStruct(downloadUrl string, proxyTimeout int64, maxBuferredChunk int64, chunkSize int64, startOffset int64, endOffset int64, numTasks int64) *ProxyDownloadStruct {
	return &ProxyDownloadStruct{
		ProxyRunning:         true,
		MaxBufferedChunk:     int64(maxBuferredChunk),
		ProxyTimeout:         proxyTimeout,
		ReadyChunkQueue:      make(chan *Chunk, maxBuferredChunk),
		ProxyMutex:           &sync.Mutex{},
		ChunkSize:            chunkSize,
		NextChunkStartOffset: startOffset,
		CurrentOffset:        startOffset,
		EndOffset:            endOffset,
		ThreadCount:          numTasks,
		DownloadUrl:          downloadUrl,
	}
}

func ConcurrentDownload(downloadUrl string, rangeStart int64, rangeEnd int64, fileSize int64, splitSize int64, numTasks int64, emitter *base.Emitter, cacheKey string, req *http.Request) {
	totalLength := rangeEnd - rangeStart + 1
	numSplits := int64(totalLength/int64(splitSize)) + 1
	if numSplits > int64(numTasks) {
		numSplits = int64(numTasks)
	}

	blockSize := numSplits * splitSize
	log.Debugf("ConcurrentDownload downloadUrl:%+v, start:%+v, end:%+v, contentLength:%+v, splitSize:%+v, numSplits:%+v, blockSize:%+v, numTasks:%+v", downloadUrl, rangeStart, rangeEnd, totalLength, splitSize, numSplits, blockSize, numSplits)
	maxChunks := int64(128*1024*1024) / splitSize
	p := newProxyDownloadStruct(downloadUrl, 10, maxChunks, splitSize, rangeStart, rangeEnd, numTasks)
	for i := 0; i < int(numSplits); i++ {
		go p.ProxyWorker(req)
	}

	defer func() {
		_, found := mediaCache.Get(cacheKey)
		if found {
			mediaCache.DecrementInt(cacheKey, 1)
		}
		p.ProxyStop()
		p = nil
	}()

	loopCount := 0
	initLevel, _ := mediaCache.Get(cacheKey)

	log.Debugf("loopCount:%+v, initLevel:%+v", loopCount, initLevel)

	for {
		if loopCount > 0 {
			currentLevel, found := mediaCache.Get(cacheKey)

			log.Debugf("loopCount:%+v,", loopCount)
			log.Debugf("currentLevel:%+v, found:%+v", currentLevel, found)

			if found {
				if currentLevel.(int) > initLevel.(int) {
					p.ProxyStop()
					emitter.Close()
					log.Debugf("currentLevel:%+v is large than initLevel:%+v, sleep and return", currentLevel, initLevel)
					time.Sleep(time.Duration((currentLevel.(int)-initLevel.(int))*2) * time.Second)
					return
				}
			}
		}
		buffer := p.ProxyRead()

		if len(buffer) == 0 {
			p.ProxyStop()
			emitter.Close()
			log.Debugf("proxyread fail")
			buffer = nil
			return
		}

		_, ok := emitter.Write(buffer)

		if ok != nil {
			p.ProxyStop()
			emitter.Close()
			log.Debugf("emitter Write fail:%+v", ok)
			buffer = nil
			return
		}

		if loopCount%100 == 0 {
			log.Debugf("service %d block", loopCount)
		}
		loopCount += 1

		if p.CurrentOffset >= rangeEnd {
			p.ProxyStop()
			emitter.Close()
			log.Debugf("all block service finish blockcount:%+v, length:%+v", loopCount, totalLength)
			buffer = nil
			return
		}
		buffer = nil
	}
}

func (p *ProxyDownloadStruct) ProxyRead() []byte {
	// 判断文件是否下载结束
	if p.CurrentOffset > p.EndOffset {
		p.ProxyStop()
		return nil
	}

	// 获取当前的chunk的数据
	var currentChunk *Chunk
	select {
	case currentChunk = <-p.ReadyChunkQueue:
		break
	case <-time.After(time.Duration(p.ProxyTimeout) * time.Second):
		log.Debugf("ProxyRead timeout")
		p.ProxyStop()
		return nil
	}

	for {
		if !p.ProxyRunning {
			break
		}
		buffer := currentChunk.get()
		if len(buffer) > 0 {
			p.CurrentOffset += int64(len(buffer))
			currentChunk = nil
			return buffer
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	currentChunk = nil
	return nil
}

func (p *ProxyDownloadStruct) ProxyStop() {
	p.ProxyRunning = false
	var currentChunk *Chunk
	for {
		select {
		case currentChunk = <-p.ReadyChunkQueue:
			currentChunk.buffer = nil
			currentChunk = nil
		case <-time.After(1 * time.Second):
			return
		}
	}
}

func (p *ProxyDownloadStruct) ProxyWorker(req *http.Request) {
	for {
		if !p.ProxyRunning {
			break
		}
		// 生成下一个chunk
		var chunk *Chunk
		p.ProxyMutex.Lock()
		chunk = nil
		startOffset := p.NextChunkStartOffset
		p.NextChunkStartOffset += p.ChunkSize
		if startOffset <= p.EndOffset {
			endOffset := startOffset + p.ChunkSize - 1
			if endOffset > p.EndOffset {
				endOffset = p.EndOffset
			}
			chunk = newChunk(startOffset, endOffset)

			p.ReadyChunkQueue <- chunk
		}
		p.ProxyMutex.Unlock()

		retryCount := 5
		if startOffset < int64(1048576) || (p.EndOffset-startOffset)/p.EndOffset*1000 < 2 {
			retryCount = 7
		}

		// 所有chunk已下载完
		if chunk == nil {
			break
		}

		for {
			if !p.ProxyRunning {
				break
			} else {
				// 过多的数据未被取走，先休息一下，避免内存溢出
				if chunk.startOffset-p.CurrentOffset >= p.ChunkSize*p.MaxBufferedChunk {
					time.Sleep(1 * time.Second)
				} else {
					break
				}
			}
		}

		for {
			if !p.ProxyRunning {
				break
			} else {
				// 建立连接
				rangeStr := fmt.Sprintf("bytes=%d-%d", chunk.startOffset, chunk.endOffset)
				newHeader := make(map[string][]string)
				for name, value := range req.Header {
					if !shouldFilterHeaderName(name) {
						newHeader[name] = value
					}
				}
				var resp *resty.Response
				var err error
				for retry := 0; retry < retryCount; retry++ {
					resp, err = base.RestyClient.
						SetTimeout(5*time.Second).
						SetRetryCount(1).
						R().
						SetHeaderMultiValues(newHeader).
						SetHeader("Range", rangeStr).
						Get(p.DownloadUrl)
					if err != nil {
						log.Debugf("downloadUrl:%+v range=%d-%d fail:%+v", p.DownloadUrl, chunk.startOffset, chunk.endOffset, err)
						time.Sleep(1 * time.Second)
						resp = nil
						continue
						// break
					}
					if !strings.HasPrefix(resp.Status(), "20") {
						log.Debugf("downloadUrl:%+v range=%d-%d non-20x status:%+v", p.DownloadUrl, chunk.startOffset, chunk.endOffset, resp.Status())
						resp = nil
						p.ProxyStop()
						return
						// break
					}
					break
				}
				if err != nil {
					resp = nil
					p.ProxyStop()
					return
				}

				// 接收数据
				buffer := make([]byte, chunk.endOffset-chunk.startOffset+1)
				copy(buffer, resp.Body())
				chunk.put(buffer)
				resp = nil
				break
			}
		}
	}
}

func handleMethod(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		// 处理 GET 请求
		log.Println("正在 GET 请求")
		// 检查查询参数是否为空
		if req.URL.RawQuery == "" {
			// 获取嵌入的 index.html 文件
			index, err := indexHTML.Open("static/index.html")
			if err != nil {
				http.Error(w, fmt.Sprintf("读取index.html错误: %v", err), http.StatusBadRequest)
				return
			}
			defer index.Close()

			// 将嵌入的文件内容复制到响应中
			io.Copy(w, index)

		} else {
			// 如果有查询参数，则返回自定义的内容
			handleGet(w, req)
		}
	case http.MethodPost:
		// 处理 POST 请求
		log.Println("正在处理 POST 请求")
		handlePost(w, req)
	default:
		// 处理其他方法的请求
		log.Println("Received a request with method:", req.Method)
		http.Error(w, fmt.Sprintf("不支持 %s 请求", req.Method), http.StatusMethodNotAllowed)
	}
}

func handlePost(w http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()

	var postUrl string
	query := req.URL.Query()
	postUrl = query.Get("url")
	strForm := query.Get("form")
	strHeader := query.Get("header")

	if postUrl != "" {
		if strForm == "base64" {
			bytesPostUrl, err := base64.StdEncoding.DecodeString(postUrl)
			if err != nil {
				http.Error(w, fmt.Sprintf("无效的 Base64 URL: %v", err), http.StatusBadRequest)
				return
			}
			postUrl = string(bytesPostUrl)
		}
	} else {
		log.Debugf("缺少URL参数")
		time.Sleep(1 * time.Second)
		http.Error(w, "缺少URL参数", 500)
		return
	}

	// 处理自定义 header
	if strHeader != "" {
		var header map[string]string
		if strForm == "base64" {
			bytesStrHeader, err := base64.StdEncoding.DecodeString(strHeader)
			if err != nil {
				http.Error(w, fmt.Sprintf("无效的Base64 Headers: %v", err), http.StatusBadRequest)
				return
			}
			strHeader = string(bytesStrHeader)
		}
		err := json.Unmarshal([]byte(strHeader), &header)
		if err != nil {
			http.Error(w, fmt.Sprintf("Headers Json格式化错误: %v", err), http.StatusBadRequest)
			return
		}
		for key, value := range header {
			req.Header.Set(key, value)
		}
	}

	// 构建新的 URL
	for parameterName := range query {
		if parameterName == "url" || parameterName == "form" || parameterName == "thread" || parameterName == "size" || parameterName == "base64" || parameterName == "header" {
			continue
		}
		postUrl = postUrl + "&" + parameterName + "=" + query.Get(parameterName)
	}

	// 构建新的 header
	newHeader := make(map[string][]string)
	for name, value := range req.Header {
		if !shouldFilterHeaderName(name) {
			newHeader[name] = value
		}
	}
	log.Debugf("url:%+v, header:%+v", postUrl, req.Header)

	// 读取请求体以记录
	postBody, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("读取 Post 参数错误: %v", err), http.StatusBadRequest)
		return
	}

	// 使用 RestyClient 转发请求
	resp, err := base.RestyClient.
		SetTimeout(10 * time.Second).
		SetRetryCount(3).
		R().
		SetBody(postBody).
		SetHeaderMultiValues(newHeader).
		Post(postUrl)
	if err != nil {
		log.Debugf("postUrl:%v fail:%+v", postUrl, err)
		time.Sleep(1 * time.Second)
		http.Error(w, err.Error(), http.StatusBadRequest)
		resp = nil
		return
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		time.Sleep(1 * time.Second)
		http.Error(w, resp.Status(), resp.StatusCode())
		resp = nil
		return
	}

	// 处理响应
	w.Header().Set("Connection", "close")
	for name, value := range resp.Header() {
		w.Header().Set(name, strings.Join(value, ","))
	}
	w.WriteHeader(resp.StatusCode())
	bodyReader := bytes.NewReader(resp.Body())
	io.Copy(w, bodyReader)
}

func handleGet(w http.ResponseWriter, req *http.Request) {
	pw := bufio.NewWriterSize(w, 128*1024)
	defer func() {
		if pw.Buffered() > 0 {
			pw.Flush()
		}
	}()
	var downloadUrl string
	query := req.URL.Query()
	downloadUrl = query.Get("url")
	strForm := query.Get("form")
	strHeader := query.Get("header")
	strThread := req.URL.Query().Get("thread")
	strSplitSize := req.URL.Query().Get("size")
	if downloadUrl != "" {
		if strForm == "base64" {
			bytesDownloadUrl, err := base64.StdEncoding.DecodeString(downloadUrl)
			if err != nil {
				http.Error(w, fmt.Sprintf("无效的 Base64 URL: %v", err), http.StatusBadRequest)
				return
			}
			downloadUrl = string(bytesDownloadUrl)
		}
	} else {
		log.Debugf("缺少URL参数")
		time.Sleep(1 * time.Second)
		http.Error(w, "缺少URL参数", 404)
		return
	}
	if strHeader != "" {
		var header map[string]string
		if strForm == "base64" {
			bytesStrHeader, err := base64.StdEncoding.DecodeString(strHeader)
			if err != nil {
				http.Error(w, fmt.Sprintf("无效的Base64 Headers: %v", err), http.StatusBadRequest)
				return
			}
			strHeader = string(bytesStrHeader)
		}
		err := json.Unmarshal([]byte(strHeader), &header)
		if err != nil {
			http.Error(w, fmt.Sprintf("Headers Json格式化错误: %v", err), http.StatusBadRequest)
			return
		}
		for key, value := range header {
			req.Header.Set(key, value)
		}
	}
	for parameterName := range query {
		if parameterName == "url" || parameterName == "form" || parameterName == "thread" || parameterName == "size" || parameterName == "base64" || parameterName == "header" {
			continue
		}
		downloadUrl = downloadUrl + "&" + parameterName + "=" + query.Get(parameterName)
	}
	log.Debugf("url:%+v, header:%+v", downloadUrl, req.Header)
	requestRange := req.Header.Get("Range")
	if requestRange == "" {
		requestRange = req.Header.Get("range")
	}
	rangeRegex := regexp.MustCompile(`bytes= *([0-9]+) *- *([0-9]*)`)
	var rangeStart, rangeEnd = int64(0), int64(0)
	matchGroup := rangeRegex.FindStringSubmatch(requestRange)
	if matchGroup != nil {
		rangeStart, _ = strconv.ParseInt(matchGroup[1], 10, 64)
		if len(matchGroup) > 2 {
			rangeEnd, _ = strconv.ParseInt(matchGroup[2], 10, 64)
		} else {
			rangeEnd = int64(0)
		}
	}
	headersKey := downloadUrl + "#PROXYDOWNLOAD_HEADERS"
	cacheTimeKey := downloadUrl + "#PROXYDOWNLOAD_LASTMODIFIED"
	lastModifiedI, found := mediaCache.Get(cacheTimeKey)
	var lastModified int64
	if found {
		lastModified = lastModifiedI.(int64)
	} else {
		lastModified = int64(0)
	}
	var responseHeaders interface{}

	curTime := time.Now().Unix()
	responseHeaders, found = mediaCache.Get(headersKey)
	newHeader := make(map[string][]string)
	for name, value := range req.Header {
		if !shouldFilterHeaderName(name) {
			newHeader[name] = value
		}
	}
	if !found || curTime-lastModified > 60 {
		resp, err := base.RestyClient.
			SetTimeout(10*time.Second).
			SetRetryCount(3).
			R().
			SetOutput(os.DevNull).
			SetHeaderMultiValues(newHeader).
			SetHeader("Range", "bytes=0-1023").
			Get(downloadUrl)
		if err != nil {
			log.Debugf("downloadUrl:%v fail:%+v", downloadUrl, err)
			time.Sleep(1 * time.Second)
			http.Error(w, err.Error(), http.StatusBadRequest)
			resp = nil
			return
		}
		if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
			time.Sleep(1 * time.Second)
			http.Error(w, resp.Status(), resp.StatusCode())
			resp = nil
			return
		}
		log.Debugf("resp.header:%+v", resp.Header())
		responseHeaders = resp.Header()

		if responseHeaders.(http.Header).Get("Content-Length") == "" || responseHeaders.(http.Header).Get("content-length") == "" {
			responseHeaders.(http.Header).Add("Content-Length", strconv.FormatInt(resp.Size(), 10))
		}

		tmpContentRange := responseHeaders.(http.Header).Get("Content-Range")
		if tmpContentRange == "" {
			tmpContentRange = responseHeaders.(http.Header).Get("content-range")
		}
		tmpContentSize := int64(0)
		if tmpContentRange != "" {
			matcher := regexp.MustCompile(`.*/([0-9]+)`).FindStringSubmatch(tmpContentRange)
			if matcher != nil {
				tmpContentSize, _ = strconv.ParseInt(matcher[1], 10, 64)
			} else {
				http.Error(w, "Headers缺少 Content-Range 参数", http.StatusBadRequest)
				return
			}
		} else {
			tmpContentSize = resp.Size()
		}
		responseHeaders.(http.Header).Set("Content-Length", strconv.FormatInt(tmpContentSize, 10))
		mediaCache.Set(headersKey, responseHeaders, 1800*time.Second)
		mediaCache.Set(cacheTimeKey, curTime, 1800*time.Second)
	}
	ContentLength := responseHeaders.(http.Header).Get("Content-Length")
	if ContentLength == "" {
		ContentLength = responseHeaders.(http.Header).Get("content-length")
	}
	contentLength := int64(0)
	if ContentLength != "" {
		contentLength, _ = strconv.ParseInt(ContentLength, 10, 64)
	}
	ContentRange := responseHeaders.(http.Header).Get("Content-Range")
	if ContentRange == "" {
		ContentRange = responseHeaders.(http.Header).Get("content-range")
	}
	contentSize := int64(0)
	var noContentRange bool
	if ContentRange != "" {
		matcher := regexp.MustCompile(`.*/([0-9]+)`).FindStringSubmatch(ContentRange)
		if matcher != nil {
			contentSize, _ = strconv.ParseInt(matcher[1], 10, 64)
		} else {
			http.Error(w, "Headers缺少 Content-Range 参数", http.StatusBadRequest)
			return
		}
	} else {
		noContentRange = true
	}
	if contentSize == 0 {
		contentSize = contentLength
	}
	if rangeEnd == int64(0) {
		rangeEnd = contentSize - 1
	}
	if noContentRange {
		strThread = "1"
		log.Debugf("rangestart:%+v, rangeend:%+v, contentlength:%+v, start concurrentDownload", rangeStart, rangeEnd, rangeEnd-rangeStart+1)
	} else {
		log.Debugf("range:%+v, rangestart:%+v, rangeend:%+v, contentlength:%+v, start concurrentDownload", requestRange, rangeStart, rangeEnd, rangeEnd-rangeStart+1)
	}
	rp, wp := io.Pipe()
	cacheKey := downloadUrl + "#PROXYDOWNLOAD_LOCK"
	_, found = mediaCache.Get(cacheKey)
	if !found {
		mediaCache.Set(cacheKey, 0, 14400*time.Second)
	}
	mediaCache.IncrementInt(cacheKey, 1)
	var splitSize int64
	var numTasks int64
	splitSize = int64(128 * 1024)
	numTasks = 64
	if strThread != "" {
		numTasks, _ = strconv.ParseInt(strThread, 10, 64)
	}
	if numTasks <= 0 {
		numTasks = 1
	}
	if numTasks <= 4 {
		splitSize = int64(2048 / numTasks * 1024)
	}
	if strSplitSize != "" {
		splitSize, _ = strconv.ParseInt(strSplitSize, 10, 64)
	}
	if rangeEnd-rangeStart > 512*1024*1024 {
		if contentSize < 1*1024*1024*1024 {
			if numTasks > 16 {
				numTasks = 16
			}
		} else if contentSize < 4*1024*1024*1024 {
			if numTasks > 32 {
				numTasks = 32
			}
		} else if contentSize < 16*1024*1024*1024 {
			if numTasks > 64 {
				numTasks = 64
			}
		}
	}
	emitter := base.NewEmitter(rp, wp)
	go ConcurrentDownload(downloadUrl, rangeStart, rangeEnd, contentSize, splitSize, numTasks, emitter, cacheKey, req)

	statusCode := 206
	if matchGroup == nil {
		statusCode = 200
	}
	contentType := responseHeaders.(http.Header).Get("Content-Type")
	if contentType == "" {
		contentType = responseHeaders.(http.Header).Get("content-type")
	}
	contentDisposition := strings.ToLower(responseHeaders.(http.Header).Get("Content-Disposition"))
	if contentDisposition == "" {
		contentDisposition = strings.ToLower(responseHeaders.(http.Header).Get("content-disposition"))
	}
	if contentDisposition != "" {
		tmpReg := regexp.MustCompile(`^.*filename=\"([^\"]+)\".*$`)
		if tmpReg.MatchString(contentDisposition) {
			contentDisposition = tmpReg.ReplaceAllString(contentDisposition, "$1")
		}
	} else {
		contentDisposition = fmt.Sprintf("attachment; filename=%q", downloadUrl[strings.LastIndex(downloadUrl, "/")+1:])
	}
	if strings.HasSuffix(contentDisposition, ".webm") {
		contentType = "video/webm"
	} else if strings.HasSuffix(contentDisposition, ".avi") {
		contentType = "video/x-msvideo"
	} else if strings.HasSuffix(contentDisposition, ".wmv") {
		contentType = "video/x-ms-wmv"
	} else if strings.HasSuffix(contentDisposition, ".flv") {
		contentType = "video/x-flv"
	} else if strings.HasSuffix(contentDisposition, ".mov") {
		contentType = "video/quicktime"
	} else if strings.HasSuffix(contentDisposition, ".mkv") {
		contentType = "video/x-matroska"
	} else if strings.HasSuffix(contentDisposition, ".ts") {
		contentType = "video/mp2t"
	} else if strings.HasSuffix(contentDisposition, ".mpeg") || strings.HasSuffix(contentDisposition, ".mpg") {
		contentType = "video/mpeg"
	} else if strings.HasSuffix(contentDisposition, ".3gpp") || strings.HasSuffix(contentDisposition, ".3gp") {
		contentType = "video/3gpp"
	} else if strings.HasSuffix(contentDisposition, ".mp4") || strings.HasSuffix(contentDisposition, ".m4s") {
		contentType = "video/mp4"
	}
	for name, value := range responseHeaders.(http.Header) {
		if strings.EqualFold(strings.ToLower(name), "content-type") || strings.EqualFold(strings.ToLower(name), "content-length") || strings.EqualFold(strings.ToLower(name), "content-range") || strings.EqualFold(strings.ToLower(name), "proxy-connection") {
			continue
		}
		w.Header().Set(name, strings.Join(value, ","))
	}
	w.Header().Del("content-type")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", contentDisposition)
	w.Header().Del("content-length")
	w.Header().Set("Content-Length", fmt.Sprint(rangeEnd-rangeStart+1))
	if !noContentRange {
		w.Header().Del("content-range")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rangeStart, rangeEnd, contentSize))
	}
	w.Header().Set("Connection", "close")
	w.WriteHeader(statusCode)
	io.Copy(pw, emitter)
	defer func() {
		emitter.Close()
		log.Debugf("handleGet defer emitter close")
	}()
}

func shouldFilterHeaderName(key string) bool {
	if len(strings.TrimSpace(key)) == 0 {
		return false
	}
	key = strings.ToLower(key)
	return key == "range" || key == "host" || key == "http-client-ip" || key == "remote-addr" || key == "accept-encoding"
}

func main() {
	// 定义 dns 和 debug 命令行参数
	dns := flag.String("dns", "1.1.1.1:53", "DNS resolver IP:port")
	port := flag.String("port", "10078", "Proxy's port")
	debug := flag.Bool("debug", false, "Enable debug mode")
	flag.Parse()

	// 忽略 SIGPIPE 信号
	signal.Ignore(syscall.SIGPIPE)

	// 设置日志输出和级别
	log.SetOutput(os.Stdout)
	if *debug {
		log.SetLevel(log.DebugLevel)
		log.Info("Debug mode enabled")
	} else {
		log.SetLevel(log.InfoLevel)
	}
	log.Printf("proxy listen on %s.\n", *port)

	// 设置 DNS 解析器 IP
	base.DnsResolverIP = *dns
	base.InitClient()
	var s = http.Server{
		Addr:    ":" + *port,
		Handler: http.HandlerFunc(handleMethod),
	}
	s.SetKeepAlivesEnabled(false)
	s.ListenAndServe()
}
