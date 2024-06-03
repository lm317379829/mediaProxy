# mediaProxy
一个基于Panda Groove大佬源码的多媒体转发程序

## 使用说明
./mediaProxy 运行程序

get或post http://ip:port/?thread=线程数&form=url与header编码格式&url=链接&header=所需header

<table>
  <thead>
    <tr>
      <th style="text-align:center;">参数</th>
      <th style="text-align:center;">描述</th>
      <th style="text-align:center;">默认值</th>
      <th style="text-align:center;">示例</th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td style="text-align:center;">debug</td>
      <td style="text-align:center;">进入调试模式</td>
      <td style="text-align:center;">无</td>
      <td style="text-align:center;">-debug</td>
    </tr>
    <tr>
      <td style="text-align:center;">port</td>
      <td style="text-align:center;">指定程序端口</td>
      <td style="text-align:center;">10078</td>
      <td style="text-align:center;">-port 10079</td>
    </tr>
    <tr>
      <td style="text-align:center;">dns</td>
      <td style="text-align:center;">指定dns服务器</td>
      <td style="text-align:center;">1.1.1.1:53</td>
      <td style="text-align:center;">-dns 127.0.0.1:5335</td>
    </tr>
  </tbody>
</table>

## 链接参数
header和url可进行base64编码，以避免sni阻断
<table>
  <thead>
    <tr>
      <th style="text-align:center;">参数</th>
      <th style="text-align:center;">类型</th>
      <th style="text-align:center;">描述</th>
      <th style="text-align:center;">默认</th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td style="text-align:center;">size</td>
      <td style="text-align:center;">可选</td>
      <td style="text-align:center;">单线程下载数据大小，可动态调节</td>
      <td style="text-align:center;">128K，线程数小于4时，为 2048/线程数 K</td>
    </tr>
    <tr>
      <td style="text-align:center;">thread</td>
      <td style="text-align:center;">可选</td>
      <td style="text-align:center;">并发线程数</td>
      <td style="text-align:center;">动态调节</td>
    </tr>
    <tr>
      <td style="text-align:center;">form</td>
      <td style="text-align:center;">可选</td>
      <td style="text-align:center;">URL与header编码方式，可指定为<code>base64</code>，防止某些SNI阻断，默认<code>urlcode</code>编码</td>
      <td style="text-align:center;">urlcode</td>
    </tr>
    <tr>
      <td style="text-align:center;">header</td>
      <td style="text-align:center;">可选</td>
      <td style="text-align:center;">POST或GET所用的headers，采用JSON格式</td>
      <td style="text-align:center;"><code>{"User-Agent": "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.198 Safari/537.36"}</code></td>
    </tr>
    <tr>
      <td style="text-align:center;">url</td>
      <td style="text-align:center;">必要</td>
      <td style="text-align:center;">POST或GET的目标地址</td>
      <td style="text-align:center;">无</td>
    </tr>
  </tbody>
</table>
