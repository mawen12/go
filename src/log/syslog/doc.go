// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package syslog provides a simple interface to the system log
// service. It can send messages to the syslog daemon using UNIX
// domain sockets, UDP or TCP.
//
// Only one call to Dial is necessary. On write failures,
// the syslog client will attempt to reconnect to the server
// and write again.
//
// The syslog package is frozen and is not accepting new features.
// Some external packages provide more functionality. See:
//
//	https://godoc.org/?q=syslog

// syslog 包提供一个简单的接口来访问系统日志服务。
// 它可以使用 UNIX 域套接字、UDP 或 TCP 向 syslog 守护进程发送消息。
//
// 只需要调用一次 Dial 即可。在写入失败是，syslog 客户端将尝试重连服务器并再次写入。
//
// syslog 包已冻结，不再接收新功能。一些外部包提供了更多功能。请参阅：
//
// https://godoc.org/?q=syslog
package syslog

// BUG(brainman): This package is not implemented on Windows. As the
// syslog package is frozen, Windows users are encouraged to
// use a package outside of the standard library. For background,
// see https://golang.org/issue/1108.

// BUG(akumar): This package is not implemented on Plan 9.
