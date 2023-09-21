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


