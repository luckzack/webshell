# webshell

通常在访问远程服务器时一般是通过ssh协议来远程登录，本地使用的客户端如如mac的iterm或windows的xshell，这样安装起来也比较麻烦，有没有想过通过浏览器就能实现操作服务器呢？本文介绍下基于websocket和linux pty实现的web终端及实现，下面会简称为webshell，首先给大家展示下实际效果：在启动项目`go run main.go`后，在浏览器访问本地（或实际IP）的8080端口后访问

![屏幕录制2023-05-02+09.48.19.gif](https://p9-juejin.byteimg.com/tos-cn-i-k3u1fbpfcp/35e185ff55fe4defbf5da535acd9ef09~tplv-k3u1fbpfcp-zoom-in-crop-mark:1512:0:0:0.awebp?)

# websocket协议

websocket协议是基于TCP长链接的双向通信协议，主要用于浏览器应用，实际场景包括股票交易信息实时更新、多人聊天室、弹幕、webshell等。如果基于HTTP协议实现如上的客户端-服务端双向通信，需要客户端发起多个HTTP请求去轮询结果，导致了服务端的高连接和负载以及大量重复Header传输，网络传输效率低。websocket基于一条TCP链接，客户端和服务端能独立推送和接收数据。

websocket协议和HTTP协议都是应用层协议，彼此独立，唯一的关联是websocket的握手协商是基于HTTP的upgrade机制，如浏览器想要和服务端建立websocket连接，需要发送HTTP请求如下：

```vbnet
vbnet
复制代码
GET /ws HTTP/1.1
Host: example.com
Upgrade: websocket			
Connection: Upgrade
Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==
Origin: http://example.com
Sec-WebSocket-Protocol: binary, text
Sec-WebSocket-Version: 13
```

1. `Upgrade: websocket` 和 `Connection: Upgrade`标识客户端发起了ws链接，基于HTTP实现
2. `Sec-WebSocket-Protocol: binary, text`表示本次ws链接客户端支持的应用层类型，binary、text分别表示是二进制和文本数据类型，表示如果握手成功，接下来客户端会发送这两种类型的消息。
3. `Sec-WebSocket-Key`：一个验证码机制，服务端需要根据这个值计算`Sec-WebSocket-Accept`并放到response的header中

如果服务端同意本次ws连接请求，需要发送HTTP响应如下：

```makefile
makefile
复制代码
HTTP/1.1 101 Switching Protocols
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=
```

此时就已经建立了好ws连接了，客户端和服务端可以双向通信了。下面是关于一个ws应用的抓包数据，从图中可以看到ws连接通过一次HTTP请求即可建立

![](https://p3-juejin.byteimg.com/tos-cn-i-k3u1fbpfcp/a5d0c0c684384375a84e544a14431b11~tplv-k3u1fbpfcp-zoom-in-crop-mark:1512:0:0:0.awebp)

# pty虚拟终端

pty是pseudoterminal（虚拟终端/伪终端）的简称，一般我们远程登录ssh看到的计算机信息，都是基于pty实现的。pty是一对提供双向通信的虚拟设备，一端叫pts（pseudoterminal slave），一端叫ptm（pseudoterminal master），pts端实际上就是一个经典的Linux终端。

在Linux中，有一个特殊的设备`/dev/ptmx`，就是x个ptm的意思，只要你打开`/dev/ptmx`会得到文件句柄`fd`，同时Linux系统会在`/dev/pts/`目录下创建一个设备文件，比如叫`/dev/pts/1000`，这个文件和`fd`就相当于slave和master的关系，向`fd`写入数据，会自动传递到`/dev/pts/1000`这个设备中。

假如我们启动过一个shell进程，如`/bin/bash`，然后将这个进程的stdin、stdout、stderr都设置为pts`/dev/pts/1000`的stdin、stdout、stderr，这时如果我们向`/dev/ptmx`的`fd`进行输入指令，其实就相当于在刚刚的`/bin/bash`中执行，输出结果也会通过`fd`展示，这样，我们就得到了一个类似ssh远程登陆的虚拟终端了

# 代码实现

webshell的实现原理其实就是将网络层面的websocket和机器层面的pty连接，一端是浏览器的输出输出（这点我们可以通过html的操作实现），一端就是`/bin/bash`进程了，这样我们就能在浏览器上实现和iterm一样的功能了。

下面我们会从前后端两部分讲解如何实现一个本地机器的webshell系统（主要支持MAC和linux系统）

在后端的实现上，webshell的核心就两点，websocket和pty，可以基于下面两个开源库实现：

* [github.com/gorilla/web…](https://link.juejin.cn?target=https%3A%2F%2Fgithub.com%2Fgorilla%2Fwebsocket "https://github.com/gorilla/websocket")：websocket协议的golang版本实现
* [github.com/creack/pty](https://link.juejin.cn?target=https%3A%2F%2Fgithub.com%2Fcreack%2Fpty "https://github.com/creack/pty")：pty虚拟终端的go实现

## 后端代码 - main.go

```go
go
复制代码
package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"reflect"
	"strconv"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

var (
	cmdPath string = "/bin/bash"
)

func main() {
    // 处理websocket
	http.HandleFunc("/ws", serveWs)
    // 前端静态文件
	http.Handle("/", http.FileServer(http.Dir("./")))
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
}

func serveWs(w http.ResponseWriter, r *http.Request) {
    // 启动本地的pty
	c := exec.Command(cmdPath)
	ptmx, err := pty.Start(c)
	if err != nil {
		return
	}
	defer ptmx.Close()

	conn, err := NewConn(w, r, func(msg []byte) {
        // 浏览器resize事件回调
		u, err := url.Parse(string(msg))
		if err != nil {
			return
		}
		var row, col int64
		for k, v := range u.Query() {
			switch k {
			case "row":
				row, _ = strconv.ParseInt(v[0], 10, 63)
			case "col":
				col, _ = strconv.ParseInt(v[0], 10, 63)
			}
		}
		if row == 0 || col == 0 {
			return
		}
		err = pty.Setsize(ptmx, &pty.Winsize{
			Rows: uint16(row),
			Cols: uint16(col),
		})
		if err != nil {
			return
		}
	})
	if err != nil {
		return
	}
	defer conn.Close()

    // stdin、stdout双向绑定
	go func() {
		_, _ = io.Copy(ptmx, conn.(io.Reader))
	}()
	_, _ = io.Copy(conn.(io.Writer), ptmx)
}

// websocket的协议升级以及请求处理
type conn struct {
	wsConn *websocket.Conn
	// read buffer between websocket and application
	rBuf         *bytes.Reader
	eventHandler func(msg []byte)
}

func NewConn(w http.ResponseWriter, r *http.Request, eventHandler func(msg []byte)) (io.ReadWriteCloser, error) {
	upgrader := websocket.Upgrader{}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "upgrade websocket failed")
	}
	return &conn{
		wsConn:       ws,
		eventHandler: eventHandler,
	}, nil
}

func (c *conn) Read(p []byte) (n int, err error) {
	for c.rBuf == nil || c.rBuf.Len() == 0 {
		mt, mp, err := c.wsConn.ReadMessage()
		if err != nil {
			return 0, err
		}
		if len(mp) == 0 {
			continue
		}

		switch mt {
		// only read text/binary msg
		case websocket.TextMessage, websocket.BinaryMessage:
			if len(p) > 9 && reflect.DeepEqual(mp[:9], []byte("ws://ctrl")) {
				c.eventHandler(mp)
				continue
			}
			c.rBuf = bytes.NewReader(mp)
		default:
			continue
		}
	}
	n, err = c.rBuf.Read(p)
	if err == io.EOF {
		err = nil
	}
	return n, err
}

func (c *conn) Write(p []byte) (n int, err error) {
	// choose binary msg type in here
	err = c.wsConn.WriteMessage(websocket.TextMessage, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *conn) Close() error {
	if c.wsConn != nil {
		return c.wsConn.Close()
	}
	return nil
}
```

## 前端代码 - index.html

前端采用了xterm、xterm-addon-attach、xterm-addon-fit NPM包，需要在index.html同级目录下执行`npm install xterm && npm install xterm-addon-attach && npm install xterm-addon-fit && npm install xterm-addon-webgl`

* xterm：开源终端实现方案
* xterm-addon-attach：xterm websocket支持插件
* xterm-addon-fit：xterm 屏幕适应插件
* xterm-addon-webgl：xterm webgl支持插件

index.html的16行需要切换为后端IP

```xml
xml
复制代码
<!doctype html>
<html>
  <head>
    <link rel="stylesheet" href="./node_modules/xterm/css/xterm.css" />
    <script src="./node_modules/xterm/lib/xterm.js"></script>
    <script src="./node_modules/xterm-addon-attach/lib/xterm-addon-attach.js"></script>
    <script src="./node_modules/xterm-addon-fit/lib/xterm-addon-fit.js"></script>
    <script src="./node_modules/xterm-addon-webgl/lib/xterm-addon-webgl.js"></script>
  </head>

  <body>
    <div id="terminal" style="height: 100vh;"></div>
    <script type="module">
      let openned = false;

      // 这里切换为后端真实IP
      const ws = new WebSocket('ws://{{后端IP}}:8080/ws');
      const attachAddon = new AttachAddon.AttachAddon(ws);
      const fitAddon = new FitAddon.FitAddon();
      const webgl = new WebglAddon.WebglAddon();
      var term = new Terminal();
      term.loadAddon(attachAddon);
      term.loadAddon(fitAddon);

      term.open(document.getElementById('terminal'));
      fitAddon.fit();
      term.loadAddon(webgl);
      webgl.onContextLoss = function () {
        addon.dispose();
      };

      function debounce(fn, wait) {
        let timeout = null;
        return function () {
          if (timeout !== null) {
            clearTimeout(timeout);
          }
          timeout = setTimeout(fn, wait);
        }
      }

      ws.onopen = function() {
        fitAddon.fit();
        openned = true;
        ws.send("ws://ctrl?col=" + String(term.cols) + "&row=" + String(term.rows))
        term.focus();
      };

      window.addEventListener('resize', debounce(function (event) {
        fitAddon.fit();
        if (openned) {
          ws.send("ws://ctrl?col=" + String(term.cols) + "&row=" + String(term.rows))
                  }
                  }, 1500), false);
                  </script>
  </body>
</html>
```

实际效果：用tree打印出项目的目录结构

![](https://p3-juejin.byteimg.com/tos-cn-i-k3u1fbpfcp/2714052bf4a249978340a95b56d9a3f5~tplv-k3u1fbpfcp-zoom-in-crop-mark:1512:0:0:0.awebp)
