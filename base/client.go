package base

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"time"
	"github.com/go-resty/resty/v2"
)

var (
	NoRedirectClient          *resty.Client
	NoRedirectClientWithProxy *resty.Client
	RestyClient               *resty.Client
	RestyClientWithProxy      *resty.Client
	HttpClient                *http.Client
	DnsResolverIP             string  // 初始化为空字符串
	IdleConnTimeout           = 10 * time.Second
	dnsResolverProto          = "udp"
	dnsResolverTimeoutMs      = 10000
)
var UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/87.0.4280.88 Safari/537.36"
var DefaultTimeout = time.Second * 30

func InitClient() {
	NoRedirectClient = resty.New().SetRedirectPolicy(
		resty.RedirectPolicyFunc(func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}),
	).SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true})
	NoRedirectClient.SetHeader("user-agent", UserAgent)

	NoRedirectClientWithProxy = resty.New().SetRedirectPolicy(
		resty.RedirectPolicyFunc(func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}),
	)
	NoRedirectClientWithProxy.SetHeader("user-agent", UserAgent)
	RestyClient = NewRestyClient()
	RestyClientWithProxy = NewRestyClient()
	HttpClient = NewHttpClient()
}

func NewRestyClient() *resty.Client {
	dialer := &net.Dialer{
		// Timeout: ConnectTimeout * time.Second, // 设置连接超时为
		Resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{
					Timeout: time.Duration(dnsResolverTimeoutMs) * time.Millisecond,
				}
				return d.DialContext(ctx, dnsResolverProto, DnsResolverIP)
			},
		},
	}

	transport := &http.Transport{
		DialContext: dialer.DialContext,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(certificates [][]byte, _ [][]*x509.Certificate) error {
				// 完全忽略证书验证
				return nil
			},
		},
		IdleConnTimeout: IdleConnTimeout,
	}

	client := resty.New().
		SetHeader("user-agent", UserAgent).
		SetRetryCount(3).
		SetTimeout(DefaultTimeout).
		SetTransport(transport)
	return client
}

func NewHttpClient() *http.Client {
	dialer := &net.Dialer{
		// Timeout: ConnectTimeout, // 设置连接超时为
		Resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{
					Timeout: time.Duration(dnsResolverTimeoutMs) * time.Millisecond,
				}
				return d.DialContext(ctx, dnsResolverProto, DnsResolverIP)
			},
		},
	}

	return &http.Client{
		Timeout: time.Hour * 48,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext:     dialer.DialContext,
			IdleConnTimeout: IdleConnTimeout,
		},
	}
}
