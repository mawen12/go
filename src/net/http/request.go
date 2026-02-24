// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// HTTP Request reading and parsing.

package http

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http/httptrace"
	"net/http/internal/ascii"
	"net/textproto"
	"net/url"
	urlpkg "net/url"
	"strconv"
	"strings"
	"sync"
	_ "unsafe" // for linkname

	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/idna"
)

const (
	defaultMaxMemory = 32 << 20 // 32 MB
)

// ErrMissingFile is returned by FormFile when the provided file field name
// is either not present in the request or not a file field.
var ErrMissingFile = errors.New("http: no such file")

// ProtocolError represents an HTTP protocol error.
//
// Deprecated: Not all errors in the http package related to protocol errors
// are of type ProtocolError.
type ProtocolError struct {
	ErrorString string
}

func (pe *ProtocolError) Error() string { return pe.ErrorString }

// Is lets http.ErrNotSupported match errors.ErrUnsupported.
func (pe *ProtocolError) Is(err error) bool {
	return pe == ErrNotSupported && err == errors.ErrUnsupported
}

var (
	// ErrNotSupported indicates that a feature is not supported.
	//
	// It is returned by ResponseController methods to indicate that
	// the handler does not support the method, and by the Push method
	// of Pusher implementations to indicate that HTTP/2 Push support
	// is not available.
	ErrNotSupported = &ProtocolError{"feature not supported"}

	// Deprecated: ErrUnexpectedTrailer is no longer returned by
	// anything in the net/http package. Callers should not
	// compare errors against this variable.
	ErrUnexpectedTrailer = &ProtocolError{"trailer header without chunked transfer encoding"}

	// ErrMissingBoundary is returned by Request.MultipartReader when the
	// request's Content-Type does not include a "boundary" parameter.
	ErrMissingBoundary = &ProtocolError{"no multipart boundary param in Content-Type"}

	// ErrNotMultipart is returned by Request.MultipartReader when the
	// request's Content-Type is not multipart/form-data.
	ErrNotMultipart = &ProtocolError{"request Content-Type isn't multipart/form-data"}

	// Deprecated: ErrHeaderTooLong is no longer returned by
	// anything in the net/http package. Callers should not
	// compare errors against this variable.
	ErrHeaderTooLong = &ProtocolError{"header too long"}

	// Deprecated: ErrShortBody is no longer returned by
	// anything in the net/http package. Callers should not
	// compare errors against this variable.
	ErrShortBody = &ProtocolError{"entity body too short"}

	// Deprecated: ErrMissingContentLength is no longer returned by
	// anything in the net/http package. Callers should not
	// compare errors against this variable.
	ErrMissingContentLength = &ProtocolError{"missing ContentLength in HEAD response"}
)

func badStringError(what, val string) error { return fmt.Errorf("%s %q", what, val) }

// Headers that Request.Write handles itself and should be skipped.
var reqWriteExcludeHeader = map[string]bool{
	"Host":              true, // not in Header map anyway
	"User-Agent":        true,
	"Content-Length":    true,
	"Transfer-Encoding": true,
	"Trailer":           true,
}

// A Request represents an HTTP request received by a server
// or to be sent by a client.
//
// The field semantics differ slightly between client and server
// usage. In addition to the notes on the fields below, see the
// documentation for [Request.Write] and [RoundTripper].

// 一个 Request 代表一个 HTTP 请求，可以是被服务器接收，也可以是被
// 客户端发送。
//
// 字段的语义在客户端和服务器使用之间略有不同，除了在下面的字段说明
// 中提到的内容之外，还请参阅 [Request.Write] 和 [RoundTripper] 的文档。
type Request struct {
	// Method specifies the HTTP method (GET, POST, PUT, etc.).
	// For client requests, an empty string means GET.

	// Method 指定 HTTP 方法（GET、POST、PUT 等）。
	// 对于客户端请求，空字符串表示 GET。
	Method string

	// URL specifies either the URI being requested (for server
	// requests) or the URL to access (for client requests).
	//
	// For server requests, the URL is parsed from the URI
	// supplied on the Request-Line as stored in RequestURI.  For
	// most requests, fields other than Path and RawQuery will be
	// empty. (See RFC 7230, Section 5.3)
	//
	// For client requests, the URL's Host specifies the server to
	// connect to, while the Request's Host field optionally
	// specifies the Host header value to send in the HTTP
	// request.

	// URL 指定了被请求的 URI （对服务器请求）或要访问的 URL
	// （对客户端请求）。
	//
	// 对于服务端请求，URL 是从 Request-Line 中提供的 URI
	// 解析而来的，并存储在 RequestURI 中。对于绝大部分请求，
	// URL 中除了 Path 和 RawQuery 以外的字段都是空的。（参见 RFC 7230，第 5.3 节）
	//
	// 对于客户端请求，URL 的 Host 指定了要连接的服务器，
	// 而 Request 的 Host 字段可选地指定了要在 HTTP 请求
	// 中发送的 Host 头的值。
	URL *url.URL

	// The protocol version for incoming server requests.
	//
	// For client requests, these fields are ignored. The HTTP
	// client code always uses either HTTP/1.1 or HTTP/2.
	// See the docs on Transport for details.

	// 对于服务器请求，协议版本。
	//
	// 对于客户端请求，这些字段可被忽略。HTTP 客户端代码总是
	// 使用 HTTP/1.1 或 HTTP/2。详情请参阅 Transport 的文档。
	Proto      string // "HTTP/1.0"
	ProtoMajor int    // 1
	ProtoMinor int    // 0

	// Header contains the request header fields either received
	// by the server or to be sent by the client.
	//
	// If a server received a request with header lines,
	//
	//	Host: example.com
	//	accept-encoding: gzip, deflate
	//	Accept-Language: en-us
	//	fOO: Bar
	//	foo: two
	//
	// then
	//
	//	Header = map[string][]string{
	//		"Accept-Encoding": {"gzip, deflate"},
	//		"Accept-Language": {"en-us"},
	//		"Foo": {"Bar", "two"},
	//	}
	//
	// For incoming requests, the Host header is promoted to the
	// Request.Host field and removed from the Header map.
	//
	// HTTP defines that header names are case-insensitive. The
	// request parser implements this by using CanonicalHeaderKey,
	// making the first character and any characters following a
	// hyphen uppercase and the rest lowercase.
	//
	// For client requests, certain headers such as Content-Length
	// and Connection are automatically written when needed and
	// values in Header may be ignored. See the documentation
	// for the Request.Write method.

	// Header 包含了服务器接收或客户端发送的请求头字段。
	//
	// 如果服务器接收到一个请求，包含以下请求行：
	//
	// 	Host: example.com
	// 	accept-encoding: gzip, deflate
	// 	Accept-Language: en-us
	// 	fOO: Bar
	// 	foo: two
	//
	// then
	//
	//  Header = map[string][]string{
	// 		"Accept-Encoding": {"gzip, deflate"},
	//      "Accept-Language": {"en-us"},
	//      "Foo": {"Bar", "two"},
	//  }
	//
	// 对于传入的请求，Host 头被提升到 Request.Host 字段，
	// 并从 Header map 中移除。
	//
	// HTTP 定义了头名称是不区分大小写的。请求解析器通过
	// 使用 CanonicalHeaderKey 来实现这一点，是第一个字符
	// 和任何连字符（-）后跟着的字符大写，其他字符小写。
	//
	// 对于客户端请求，某些头如 Content-Length 和 Connection
	// 在需要时会自动写入，Header 中值可能会被忽略。查看
	// Request.Write 方法的文档。
	Header Header

	// Body is the request's body.
	//
	// For client requests, a nil body means the request has no
	// body, such as a GET request. The HTTP Client's Transport
	// is responsible for calling the Close method.
	//
	// For server requests, the Request Body is always non-nil
	// but will return EOF immediately when no body is present.
	// The Server will close the request body. The ServeHTTP
	// Handler does not need to.
	//
	// Body must allow Read to be called concurrently with Close.
	// In particular, calling Close should unblock a Read waiting
	// for input.

	// Body 是请求的主体。
	//
	// 对于客户端请求，一个 nil 的 body 意味着请求没有 body。
	// 例如 GET 请求。HTTP 客户端的 Transport 负责调用 Close 方法。
	//
	// 对于服务端请求，请求体总是非 nil 的，但当没有 body 是会立即
	// 返回 EOF。服务器会关闭请求体。ServeHTTP Handler 不需要。
	//
	// Bodu 必须允许 Read 与 Close 并发调用。特别是，调用 Close
	// 应该解除一个等待输入的 Read。
	Body io.ReadCloser

	// GetBody defines an optional func to return a new copy of
	// Body. It is used for client requests when a redirect requires
	// reading the body more than once. Use of GetBody still
	// requires setting Body.
	//
	// For server requests, it is unused.

	// GetBody 定义一个可选的函数来返回一个新的 Body 副本。
	// 当需要重定向时，客户端请求需要多次读取 body 时使用。
	// 使用 GetBody 仍然需要设置 Body。
	//
	// 对于服务器请求，它未被使用。
	GetBody func() (io.ReadCloser, error)

	// ContentLength records the length of the associated content.
	// The value -1 indicates that the length is unknown.
	// Values >= 0 indicate that the given number of bytes may
	// be read from Body.
	//
	// For client requests, a value of 0 with a non-nil Body is
	// also treated as unknown.

	// ContentLength 记录关联内容的长度，值 -1 表示长度位置。
	// 值 >= 0 表示可以从 body 中读取指定数量的字节。
	//
	// 对于客户端请求，一个值为 0 且 Body 非 nil 的请求也被
	// 视为长度未知。
	ContentLength int64

	// TransferEncoding lists the transfer encodings from outermost to
	// innermost. An empty list denotes the "identity" encoding.
	// TransferEncoding can usually be ignored; chunked encoding is
	// automatically added and removed as necessary when sending and
	// receiving requests.

	// TransferEncoding 列出了从最外层到最内层的传输编码。空列表表示
	// "identity" 编码。TransferEncoding 可以通常被忽略，因为在发送
	// 和接收请求时，chunked 编码会根据需要自动添加和删除。
	TransferEncoding []string

	// Close indicates whether to close the connection after
	// replying to this request (for servers) or after sending this
	// request and reading its response (for clients).
	//
	// For server requests, the HTTP server handles this automatically
	// and this field is not needed by Handlers.
	//
	// For client requests, setting this field prevents re-use of
	// TCP connections between requests to the same hosts, as if
	// Transport.DisableKeepAlives were set.

	// Close 指示是否在回复此请求后（对于服务器）或
	// 在发送该请求后然后读取其响应后（对于客户端）关闭连接。
	//
	// 对于服务端请求，HTTP 服务器会自动处理这个字段，Handler 不需要再去处理。
	//
	// 对于客户端请求，设置这个字段会阻止在对同一主机的请求之间重用
	// TCP 连接，就像设置了 Transport.DisableKeepAlives 一样。
	Close bool

	// For server requests, Host specifies the host on which the
	// URL is sought. For HTTP/1 (per RFC 7230, section 5.4), this
	// is either the value of the "Host" header or the host name
	// given in the URL itself. For HTTP/2, it is the value of the
	// ":authority" pseudo-header field.
	// It may be of the form "host:port". For international domain
	// names, Host may be in Punycode or Unicode form. Use
	// golang.org/x/net/idna to convert it to either format if
	// needed.
	// To prevent DNS rebinding attacks, server Handlers should
	// validate that the Host header has a value for which the
	// Handler considers itself authoritative. The included
	// ServeMux supports patterns registered to particular host
	// names and thus protects its registered Handlers.
	//
	// For client requests, Host optionally overrides the Host
	// header to send. If empty, the Request.Write method uses
	// the value of URL.Host. Host may contain an international
	// domain name.

	// 对于服务器请求，Host 指定了 URL 被请求的主机。对于 HTTP/1
	// （根据 RFC 7230，第 5.4 节），这可以是 Host 头的值或 URL 本身
	// 给出的主机名。对于 HTTP/2，它是 ":authority" 伪头字段的值。
	// 它可能是 "host:port" 的形式。对于国际域名，Host 可以是 Punycode
	// 或 Unicode 形式。使用 golang.org/x/net/idna 来转换成任一格式（如果需要）。
	// 为了防止 DNS 反绑定攻击，服务器 Handler 应该验证 Host 头具有一个 Header
	// 认为自己是权威的值。包含的 ServeMux 支持注册到特定主机名的模式，
	// 因此保护其注册的 Handler。
	//
	// 对于客户端请求，Host 可选地覆盖要发送的 Host 头。
	// 如果为空，Request.Write 方法使用 URL.Host 的值。
	// Host 可以包含国际域名。
	Host string

	// Form contains the parsed form data, including both the URL
	// field's query parameters and the PATCH, POST, or PUT form data.
	// This field is only available after ParseForm is called.
	// The HTTP client ignores Form and uses Body instead.

	// Form 包含了解析后的表单数据，包含 URL 字段的查询参数和
	// PATCH、POST 或 PUT 表单数据。
	// 该字段仅在调用 ParseForm 后可用。
	// HTTP 客户端忽略 Form 并使用 Body 代替。
	Form url.Values

	// PostForm contains the parsed form data from PATCH, POST
	// or PUT body parameters.
	//
	// This field is only available after ParseForm is called.
	// The HTTP client ignores PostForm and uses Body instead.

	// PostForm 包含了从 PATCH、POST 或 PUT 的 body 参数解析后的
	// 表单数据。
	//
	// 该字段仅在调用 ParseForm 后可用。
	// HTTP 客户端忽略 PostForm 并使用 Body 代替。
	PostForm url.Values

	// MultipartForm is the parsed multipart form, including file uploads.
	// This field is only available after ParseMultipartForm is called.
	// The HTTP client ignores MultipartForm and uses Body instead.

	// MultipartForm 是解析后的 multipart 表单，包含文件上传。
	// 该字段仅在调用 ParseMultipartForm 后可用。
	// HTTP 客户端忽略 MultipartForm 并使用 Body 代替。
	MultipartForm *multipart.Form

	// Trailer specifies additional headers that are sent after the request
	// body.
	//
	// For server requests, the Trailer map initially contains only the
	// trailer keys, with nil values. (The client declares which trailers it
	// will later send.)  While the handler is reading from Body, it must
	// not reference Trailer. After reading from Body returns EOF, Trailer
	// can be read again and will contain non-nil values, if they were sent
	// by the client.
	//
	// For client requests, Trailer must be initialized to a map containing
	// the trailer keys to later send. The values may be nil or their final
	// values. The ContentLength must be 0 or -1, to send a chunked request.
	// After the HTTP request is sent the map values can be updated while
	// the request body is read. Once the body returns EOF, the caller must
	// not mutate Trailer.
	//
	// Few HTTP clients, servers, or proxies support HTTP trailers.

	// Trailer 指定了在请求体后面发送的附加头。
	//
	// 对于服务器请求，Trailer map 初始化时仅包含 trailer 键，值为 nil。
	// (客户端声明它将来会发送哪些 trailers）。当 Handler 从 Body 读取时，
	// 不能引用 Trailer。当从 Body 读取返回 EOF 后，可以再次读取 Trailer，
	// 如果客户端发送了 trailers，Trailer 将包含非 nil 的值。
	//
	// 对于客户端请求，Trailer 必须初始化为一个包含将来要发送的 trailer 键的 map。
	// 值可以是 nil 或它们的最终值。ContentLength 必须是 0 或 -1,以发送一个 chunked
	// 请求。在 HTTP 请求发送后，map 值可以在请求体被读取时更新，一旦 body 返回 EOF，
	// 调用者必须不再修改 Trailer。
	//
	// 很少有 HTTP 客户端、服务器或代理支持 HTTP trailers。
	Trailer Header

	// RemoteAddr allows HTTP servers and other software to record
	// the network address that sent the request, usually for
	// logging. This field is not filled in by ReadRequest and
	// has no defined format. The HTTP server in this package
	// sets RemoteAddr to an "IP:port" address before invoking a
	// handler.
	// This field is ignored by the HTTP client.

	// RemoteAddr 允许 HTTP 服务器和其他软件记录发送请求的网络地址，
	// 通常用于日志记录。该字段不由 ReadRequest 填充，且没有定义的格式。
	// HTTP 包中的服务器在调用 Handler 之前将 RemoteAddr 设置为一个 "IP:port" 地址。
	// 该字段被 HTTP 客户端忽略。
	RemoteAddr string

	// RequestURI is the unmodified request-target of the
	// Request-Line (RFC 7230, Section 3.1.1) as sent by the client
	// to a server. Usually the URL field should be used instead.
	// It is an error to set this field in an HTTP client request.

	// RequestURI 是客户端发送给服务器的 Request-Line 中未经修改的
	// request-target（RFC 7230，第 3.1.1 节）。通常应该使用 URL 字段。
	// 在 HTTP 客户端请求中设置此字段是一个错误。
	RequestURI string

	// TLS allows HTTP servers and other software to record
	// information about the TLS connection on which the request
	// was received. This field is not filled in by ReadRequest.
	// The HTTP server in this package sets the field for
	// TLS-enabled connections before invoking a handler;
	// otherwise it leaves the field nil.
	// This field is ignored by the HTTP client.

	// TLS 允许 HTTP 服务器和其他软件记录有关接收请求的 TLS 连接的信息。
	// 该字段不应由 ReadRequest 填充。HTTP 包中的服务器在启用 TLS 的连接
	// 上设置该字段，在调用 Handler 之前；否则将该字段保持为 nil。
	// 该字段被 HTTP 客户端忽略。
	TLS *tls.ConnectionState

	// Cancel is an optional channel whose closure indicates that the client
	// request should be regarded as canceled. Not all implementations of
	// RoundTripper may support Cancel.
	//
	// For server requests, this field is not applicable.
	//
	// Deprecated: Set the Request's context with NewRequestWithContext
	// instead. If a Request's Cancel field and context are both
	// set, it is undefined whether Cancel is respected.

	// Cancel 是一个可选的通道，其关闭表示客户端请求应被视为已取消。
	// 并非所有 RoundTripper 的实现都支持 Cancel。
	//
	// 对于服务器请求，此字段不适用。
	//
	// 已废弃：请改用 NewRequestWithContext 来设置 Request 的上下文。
	// 如果一个 Request 的 Cancel 字段和上下文都被设置了，那么是否尊重 Cancel 是未定义的。
	Cancel <-chan struct{}

	// Response is the redirect response which caused this request
	// to be created. This field is only populated during client
	// redirects.

	// Response 是导致此请求被创建的重定向响应。
	// 此字段仅在客户端重定向期间填充。
	Response *Response

	// Pattern is the [ServeMux] pattern that matched the request.
	// It is empty if the request was not matched against a pattern.

	// Pattern 是与请求匹配的 [ServeMux] 模式。如果请求没有与任何模式匹配，则为空。
	Pattern string

	// ctx is either the client or server context. It should only
	// be modified via copying the whole Request using Clone or WithContext.
	// It is unexported to prevent people from using Context wrong
	// and mutating the contexts held by callers of the same request.

	// ctx 是客户端或服务端上下文，它应该只能通过使用 Clone 或 WithContext
	// 复制整个 Request 来修改。
	// 它是未导出的，以防止人们错误地使用 Context 并修改同一请求的调用者持有的上下文。
	ctx context.Context

	// The following fields are for requests matched by ServeMux.
	pat         *pattern          // the pattern that matched
	matches     []string          // values for the matching wildcards in pat
	otherValues map[string]string // for calls to SetPathValue that don't match a wildcard
}

// Context returns the request's context. To change the context, use
// [Request.Clone] or [Request.WithContext].
//
// The returned context is always non-nil; it defaults to the
// background context.
//
// For outgoing client requests, the context controls cancellation.
//
// For incoming server requests, the context is canceled when the
// client's connection closes, the request is canceled (with HTTP/2),
// or when the ServeHTTP method returns.

// Context 返回请求的上下文。要更改上下文，请使用 [Request.Clone] 或 [Request.WithContext]。
//
// 返回的上下文始终非 nil；它默认为 Background 上下文。
//
// 对于外发的客户端请求，上下文控制取消。
//
// 对于入站的服务器请求，当客户端连接关闭，请求被取消（使用 HTTP/2），或 ServeHTTP 方法返回时，
// 上下文被取消。
func (r *Request) Context() context.Context {
	if r.ctx != nil {
		return r.ctx
	}
	return context.Background()
}

// WithContext returns a shallow copy of r with its context changed
// to ctx. The provided ctx must be non-nil.
//
// For outgoing client request, the context controls the entire
// lifetime of a request and its response: obtaining a connection,
// sending the request, and reading the response headers and body.
//
// To create a new request with a context, use [NewRequestWithContext].
// To make a deep copy of a request with a new context, use [Request.Clone].

// WithContext 返回 r 的一个浅复制，其上下文更改为 ctx，提供的 ctx 必须非 nil。
//
// 对于外发的客户端请求，上下文控制请求及其响应的整个生命周期：获取连接、发送请求
// 和读取响应头和主体。
//
// 为了创建一个带有上下文的新请求，请使用 [NewRequestWithContext]。
// 要使用新上下文创建请求的深复制，请使用 [Request.Clone]。
func (r *Request) WithContext(ctx context.Context) *Request {
	if ctx == nil {
		panic("nil context")
	}
	r2 := new(Request)
	*r2 = *r
	r2.ctx = ctx
	return r2
}

// Clone returns a deep copy of r with its context changed to ctx.
// The provided ctx must be non-nil.
//
// Clone only makes a shallow copy of the Body field.
//
// For an outgoing client request, the context controls the entire
// lifetime of a request and its response: obtaining a connection,
// sending the request, and reading the response headers and body.

// Clone 返回 r  的一个深拷贝，其上下文更改为 ctx，提供的 ctx 必须非 nil。
//
// Clone 只对 Body 字段进行浅复制。
//
// 对于外发的客户端请求，上下文控制请求及其响应的整个生命周期：获取连接、发送请求
// 和读取响应头和主体。
func (r *Request) Clone(ctx context.Context) *Request {
	if ctx == nil {
		panic("nil context")
	}
	r2 := new(Request)
	*r2 = *r
	// 使用新的 ctx
	r2.ctx = ctx
	// 拷贝 URL
	r2.URL = cloneURL(r.URL)
	// 拷贝 Header
	r2.Header = r.Header.Clone()
	// 拷贝 Trailer
	r2.Trailer = r.Trailer.Clone()
	// 拷贝 TransferEncoding
	if s := r.TransferEncoding; s != nil {
		s2 := make([]string, len(s))
		copy(s2, s)
		r2.TransferEncoding = s2
	}
	// 拷贝 Form
	r2.Form = cloneURLValues(r.Form)
	// 拷贝 PostForm
	r2.PostForm = cloneURLValues(r.PostForm)
	// 拷贝 MultipartForm
	r2.MultipartForm = cloneMultipartForm(r.MultipartForm)

	// Copy matches and otherValues. See issue 61410.
	// 拷贝 matches
	if s := r.matches; s != nil {
		s2 := make([]string, len(s))
		copy(s2, s)
		r2.matches = s2
	}
	// 拷贝 otherValues
	r2.otherValues = maps.Clone(r.otherValues)
	return r2
}

// ProtoAtLeast reports whether the HTTP protocol used
// in the request is at least major.minor.

// ProtoAtLeast 报告请求中使用的 HTTP 协议是否至少为 major.minor。
func (r *Request) ProtoAtLeast(major, minor int) bool {
	return r.ProtoMajor > major ||
		r.ProtoMajor == major && r.ProtoMinor >= minor
}

// UserAgent returns the client's User-Agent, if sent in the request.

// UserAgent 返回客户端的 User-Agent，如果在请求中发送了的话。
func (r *Request) UserAgent() string {
	return r.Header.Get("User-Agent")
}

// Cookies parses and returns the HTTP cookies sent with the request.

// Cookies 解析并返回随请求发送的 HTTP cookies。
func (r *Request) Cookies() []*Cookie {
	return readCookies(r.Header, "")
}

// CookiesNamed parses and returns the named HTTP cookies sent with the request
// or an empty slice if none matched.

// CookiesNamed 解析并返回随请求发送的指定名称的 HTTP cookies，
// 如果没有匹配的 cookie，则返回一个空切片。
func (r *Request) CookiesNamed(name string) []*Cookie {
	if name == "" {
		return []*Cookie{}
	}
	return readCookies(r.Header, name)
}

// ErrNoCookie is returned by Request's Cookie method when a cookie is not found.

// ErrNoCookies 是当在 Request 的 Cookie 方法中未找到 cookie 时返回的错误。
var ErrNoCookie = errors.New("http: named cookie not present")

// Cookie returns the named cookie provided in the request or
// [ErrNoCookie] if not found.
// If multiple cookies match the given name, only one cookie will
// be returned.

// Cookie 返回请求中提供的指定名称的 cookie，如果未找到则返回 [ErrNoCookie]。
// 如果多个 cookie 匹配给定的名称，则只返回一个 cookie。
func (r *Request) Cookie(name string) (*Cookie, error) {
	if name == "" {
		return nil, ErrNoCookie
	}
	for _, c := range readCookies(r.Header, name) {
		return c, nil
	}
	return nil, ErrNoCookie
}

// AddCookie adds a cookie to the request. Per RFC 6265 section 5.4,
// AddCookie does not attach more than one [Cookie] header field. That
// means all cookies, if any, are written into the same line,
// separated by semicolon.
// AddCookie only sanitizes c's name and value, and does not sanitize
// a Cookie header already present in the request.

// AddCookie 向请求添加一个 cookie。根据 RFC 6265 第 5.4 节，AddCookie 不会附加多个 [Cookie] 头字段。
// 这意味着所有 cookie（如果有的话）都写在同一行，用分号分隔。
// AddCookie 只会清理 c 的 name 和 value，并不会清理请求中已经存在的 Cookie 头。
func (r *Request) AddCookie(c *Cookie) {
	s := fmt.Sprintf("%s=%s", sanitizeCookieName(c.Name), sanitizeCookieValue(c.Value, c.Quoted))
	if c := r.Header.Get("Cookie"); c != "" {
		r.Header.Set("Cookie", c+"; "+s)
	} else {
		r.Header.Set("Cookie", s)
	}
}

// Referer returns the referring URL, if sent in the request.
//
// Referer is misspelled as in the request itself, a mistake from the
// earliest days of HTTP.  This value can also be fetched from the
// [Header] map as Header["Referer"]; the benefit of making it available
// as a method is that the compiler can diagnose programs that use the
// alternate (correct English) spelling req.Referrer() but cannot
// diagnose programs that use Header["Referrer"].

// Referer 返回引用 URL，如果在请求中发送了的话。
//
// Referer 的拼写错误与请求本身中的拼写错误一样，是 HTTP 最早期的一个错误。
// 这个值也可以从 [Header] map 中获取，如 Header["Referer"];
// 将其作为方法提供的好处是编译器可以诊断使用了替代（正确英语）
// 拼写 req.Referrer() 的程序，但无法诊断使用 Header["Referrer"] 的程序。
func (r *Request) Referer() string {
	return r.Header.Get("Referer")
}

// multipartByReader is a sentinel value.
// Its presence in Request.MultipartForm indicates that parsing of the request
// body has been handed off to a MultipartReader instead of ParseMultipartForm.
var multipartByReader = &multipart.Form{
	Value: make(map[string][]string),
	File:  make(map[string][]*multipart.FileHeader),
}

// MultipartReader returns a MIME multipart reader if this is a
// multipart/form-data or a multipart/mixed POST request, else returns nil and an error.
// Use this function instead of [Request.ParseMultipartForm] to
// process the request body as a stream.
func (r *Request) MultipartReader() (*multipart.Reader, error) {
	if r.MultipartForm == multipartByReader {
		return nil, errors.New("http: MultipartReader called twice")
	}
	if r.MultipartForm != nil {
		return nil, errors.New("http: multipart handled by ParseMultipartForm")
	}
	r.MultipartForm = multipartByReader
	return r.multipartReader(true)
}

func (r *Request) multipartReader(allowMixed bool) (*multipart.Reader, error) {
	v := r.Header.Get("Content-Type")
	if v == "" {
		return nil, ErrNotMultipart
	}
	if r.Body == nil {
		return nil, errors.New("missing form body")
	}
	d, params, err := mime.ParseMediaType(v)
	if err != nil || !(d == "multipart/form-data" || allowMixed && d == "multipart/mixed") {
		return nil, ErrNotMultipart
	}
	boundary, ok := params["boundary"]
	if !ok {
		return nil, ErrMissingBoundary
	}
	return multipart.NewReader(r.Body, boundary), nil
}

// isH2Upgrade reports whether r represents the http2 "client preface"
// magic string.
func (r *Request) isH2Upgrade() bool {
	return r.Method == "PRI" && len(r.Header) == 0 && r.URL.Path == "*" && r.Proto == "HTTP/2.0"
}

// Return value if nonempty, def otherwise.
func valueOrDefault(value, def string) string {
	if value != "" {
		return value
	}
	return def
}

// NOTE: This is not intended to reflect the actual Go version being used.
// It was changed at the time of Go 1.1 release because the former User-Agent
// had ended up blocked by some intrusion detection systems.
// See https://codereview.appspot.com/7532043.
const defaultUserAgent = "Go-http-client/1.1"

// Write writes an HTTP/1.1 request, which is the header and body, in wire format.
// This method consults the following fields of the request:
//
//	Host
//	URL
//	Method (defaults to "GET")
//	Header
//	ContentLength
//	TransferEncoding
//	Body
//
// If Body is present, Content-Length is <= 0 and [Request.TransferEncoding]
// hasn't been set to "identity", Write adds "Transfer-Encoding:
// chunked" to the header. Body is closed after it is sent.

// Write 写入一个 HTTP/1.1 请求，以线格式写入请求头和请求体。
// 此方法会检查请求的以下字段：
//
//	Host
//	URL
//	Method（默认为 "GET"）
//	Header
//	ContentLength
//	TransferEncoding
//	Body
//
// 如果 Body 存在，Content-Length <= 0 且 [Request.TransferEncoding] 没有设置为 "identity"，
// Write 会在头中添加 "Transfer-Encoding: chunked"。发送后关闭 Body。
func (r *Request) Write(w io.Writer) error {
	return r.write(w, false, nil, nil)
}

// WriteProxy is like [Request.Write] but writes the request in the form
// expected by an HTTP proxy. In particular, [Request.WriteProxy] writes the
// initial Request-URI line of the request with an absolute URI, per
// section 5.3 of RFC 7230, including the scheme and host.
// In either case, WriteProxy also writes a Host header, using
// either r.Host or r.URL.Host.

// WriteProxy 类似于 [Request.Write]，但以 HTTP 代理期望的格式写入请求。
// 特别是，[Request.WriteProxy] 根据 RFC 7230 第 5.3 节以绝对 URI
// 的形式写入请求的初始 Request-URI 行，包括 scheme 和 host。
// 在任何情况下，WriteProxy 还会写入 Host 头，使用 r.Host 或 r.URL.Host。
func (r *Request) WriteProxy(w io.Writer) error {
	return r.write(w, true, nil, nil)
}

// errMissingHost is returned by Write when there is no Host or URL present in
// the Request.
var errMissingHost = errors.New("http: Request.Write on Request with no Host or URL set")

// extraHeaders may be nil
// waitForContinue may be nil
// always closes body
func (r *Request) write(w io.Writer, usingProxy bool, extraHeaders Header, waitForContinue func() bool) (err error) {
	trace := httptrace.ContextClientTrace(r.Context())
	if trace != nil && trace.WroteRequest != nil {
		defer func() {
			trace.WroteRequest(httptrace.WroteRequestInfo{
				Err: err,
			})
		}()
	}
	closed := false
	defer func() {
		if closed {
			return
		}
		if closeErr := r.closeBody(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	// Find the target host. Prefer the Host: header, but if that
	// is not given, use the host from the request URL.
	//
	// Clean the host, in case it arrives with unexpected stuff in it.
	host := r.Host
	if host == "" {
		if r.URL == nil {
			return errMissingHost
		}
		host = r.URL.Host
	}
	host, err = httpguts.PunycodeHostPort(host)
	if err != nil {
		return err
	}
	// Validate that the Host header is a valid header in general,
	// but don't validate the host itself. This is sufficient to avoid
	// header or request smuggling via the Host field.
	// The server can (and will, if it's a net/http server) reject
	// the request if it doesn't consider the host valid.
	if !httpguts.ValidHostHeader(host) {
		// Historically, we would truncate the Host header after '/' or ' '.
		// Some users have relied on this truncation to convert a network
		// address such as Unix domain socket path into a valid, ignored
		// Host header (see https://go.dev/issue/61431).
		//
		// We don't preserve the truncation, because sending an altered
		// header field opens a smuggling vector. Instead, zero out the
		// Host header entirely if it isn't valid. (An empty Host is valid;
		// see RFC 9112 Section 3.2.)
		//
		// Return an error if we're sending to a proxy, since the proxy
		// probably can't do anything useful with an empty Host header.
		if !usingProxy {
			host = ""
		} else {
			return errors.New("http: invalid Host header")
		}
	}

	// According to RFC 6874, an HTTP client, proxy, or other
	// intermediary must remove any IPv6 zone identifier attached
	// to an outgoing URI.
	host = removeZone(host)

	ruri := r.URL.RequestURI()
	if usingProxy && r.URL.Scheme != "" && r.URL.Opaque == "" {
		ruri = r.URL.Scheme + "://" + host + ruri
	} else if r.Method == "CONNECT" && r.URL.Path == "" {
		// CONNECT requests normally give just the host and port, not a full URL.
		ruri = host
		if r.URL.Opaque != "" {
			ruri = r.URL.Opaque
		}
	}
	if stringContainsCTLByte(ruri) {
		return errors.New("net/http: can't write control character in Request.URL")
	}
	// TODO: validate r.Method too? At least it's less likely to
	// come from an attacker (more likely to be a constant in
	// code).

	// Wrap the writer in a bufio Writer if it's not already buffered.
	// Don't always call NewWriter, as that forces a bytes.Buffer
	// and other small bufio Writers to have a minimum 4k buffer
	// size.
	var bw *bufio.Writer
	if _, ok := w.(io.ByteWriter); !ok {
		bw = bufio.NewWriter(w)
		w = bw
	}

	_, err = fmt.Fprintf(w, "%s %s HTTP/1.1\r\n", valueOrDefault(r.Method, "GET"), ruri)
	if err != nil {
		return err
	}

	// Header lines
	_, err = fmt.Fprintf(w, "Host: %s\r\n", host)
	if err != nil {
		return err
	}
	if trace != nil && trace.WroteHeaderField != nil {
		trace.WroteHeaderField("Host", []string{host})
	}

	// Use the defaultUserAgent unless the Header contains one, which
	// may be blank to not send the header.
	userAgent := defaultUserAgent
	if r.Header.has("User-Agent") {
		userAgent = r.Header.Get("User-Agent")
	}
	if userAgent != "" {
		userAgent = headerNewlineToSpace.Replace(userAgent)
		userAgent = textproto.TrimString(userAgent)
		_, err = fmt.Fprintf(w, "User-Agent: %s\r\n", userAgent)
		if err != nil {
			return err
		}
		if trace != nil && trace.WroteHeaderField != nil {
			trace.WroteHeaderField("User-Agent", []string{userAgent})
		}
	}

	// Process Body,ContentLength,Close,Trailer
	tw, err := newTransferWriter(r)
	if err != nil {
		return err
	}
	err = tw.writeHeader(w, trace)
	if err != nil {
		return err
	}

	err = r.Header.writeSubset(w, reqWriteExcludeHeader, trace)
	if err != nil {
		return err
	}

	if extraHeaders != nil {
		err = extraHeaders.write(w, trace)
		if err != nil {
			return err
		}
	}

	_, err = io.WriteString(w, "\r\n")
	if err != nil {
		return err
	}

	if trace != nil && trace.WroteHeaders != nil {
		trace.WroteHeaders()
	}

	// Flush and wait for 100-continue if expected.
	if waitForContinue != nil {
		if bw, ok := w.(*bufio.Writer); ok {
			err = bw.Flush()
			if err != nil {
				return err
			}
		}
		if trace != nil && trace.Wait100Continue != nil {
			trace.Wait100Continue()
		}
		if !waitForContinue() {
			closed = true
			r.closeBody()
			return nil
		}
	}

	if bw, ok := w.(*bufio.Writer); ok && tw.FlushHeaders {
		if err := bw.Flush(); err != nil {
			return err
		}
	}

	// Write body and trailer
	closed = true
	err = tw.writeBody(w)
	if err != nil {
		if tw.bodyReadError == err {
			err = requestBodyReadError{err}
		}
		return err
	}

	if bw != nil {
		return bw.Flush()
	}
	return nil
}

// requestBodyReadError wraps an error from (*Request).write to indicate
// that the error came from a Read call on the Request.Body.
// This error type should not escape the net/http package to users.
type requestBodyReadError struct{ error }

func idnaASCII(v string) (string, error) {
	// TODO: Consider removing this check after verifying performance is okay.
	// Right now punycode verification, length checks, context checks, and the
	// permissible character tests are all omitted. It also prevents the ToASCII
	// call from salvaging an invalid IDN, when possible. As a result it may be
	// possible to have two IDNs that appear identical to the user where the
	// ASCII-only version causes an error downstream whereas the non-ASCII
	// version does not.
	// Note that for correct ASCII IDNs ToASCII will only do considerably more
	// work, but it will not cause an allocation.
	if ascii.Is(v) {
		return v, nil
	}
	return idna.Lookup.ToASCII(v)
}

// removeZone removes IPv6 zone identifier from host.
// E.g., "[fe80::1%en0]:8080" to "[fe80::1]:8080"
func removeZone(host string) string {
	if !strings.HasPrefix(host, "[") {
		return host
	}
	i := strings.LastIndex(host, "]")
	if i < 0 {
		return host
	}
	j := strings.LastIndex(host[:i], "%")
	if j < 0 {
		return host
	}
	return host[:j] + host[i:]
}

// ParseHTTPVersion parses an HTTP version string according to RFC 7230, section 2.6.
// "HTTP/1.0" returns (1, 0, true). Note that strings without
// a minor version, such as "HTTP/2", are not valid.
func ParseHTTPVersion(vers string) (major, minor int, ok bool) {
	switch vers {
	case "HTTP/1.1":
		return 1, 1, true
	case "HTTP/1.0":
		return 1, 0, true
	}
	if !strings.HasPrefix(vers, "HTTP/") {
		return 0, 0, false
	}
	if len(vers) != len("HTTP/X.Y") {
		return 0, 0, false
	}
	if vers[6] != '.' {
		return 0, 0, false
	}
	maj, err := strconv.ParseUint(vers[5:6], 10, 0)
	if err != nil {
		return 0, 0, false
	}
	min, err := strconv.ParseUint(vers[7:8], 10, 0)
	if err != nil {
		return 0, 0, false
	}
	return int(maj), int(min), true
}

func validMethod(method string) bool {
	/*
	     Method         = "OPTIONS"                ; Section 9.2
	                    | "GET"                    ; Section 9.3
	                    | "HEAD"                   ; Section 9.4
	                    | "POST"                   ; Section 9.5
	                    | "PUT"                    ; Section 9.6
	                    | "DELETE"                 ; Section 9.7
	                    | "TRACE"                  ; Section 9.8
	                    | "CONNECT"                ; Section 9.9
	                    | extension-method
	   extension-method = token
	     token          = 1*<any CHAR except CTLs or separators>
	*/
	return isToken(method)
}

// NewRequest wraps [NewRequestWithContext] using [context.Background].
func NewRequest(method, url string, body io.Reader) (*Request, error) {
	return NewRequestWithContext(context.Background(), method, url, body)
}

// NewRequestWithContext returns a new [Request] given a method, URL, and
// optional body.
//
// If the provided body is also an [io.Closer], the returned
// [Request.Body] is set to body and will be closed (possibly
// asynchronously) by the Client methods Do, Post, and PostForm,
// and [Transport.RoundTrip].
//
// NewRequestWithContext returns a Request suitable for use with
// [Client.Do] or [Transport.RoundTrip]. To create a request for use with
// testing a Server Handler, either use the [net/http/httptest.NewRequest] function,
// use [ReadRequest], or manually update the Request fields.
// For an outgoing client request, the context
// controls the entire lifetime of a request and its response:
// obtaining a connection, sending the request, and reading the
// response headers and body. See the [Request] type's documentation for
// the difference between inbound and outbound request fields.
//
// If body is of type [*bytes.Buffer], [*bytes.Reader], or
// [*strings.Reader], the returned request's ContentLength is set to its
// exact value (instead of -1), GetBody is populated (so 307 and 308
// redirects can replay the body), and Body is set to [NoBody] if the
// ContentLength is 0.

// NewRequestWithContext 返回一个带有给定 method, URL 和可选 body 的新的 [Request]。
//
// 如果提供的 body 同时也是一个 [io.Closer]，则返回的 [Request.Body] 设置为 body，
// 并由 Client 的 Do、Post 和 PostForm 方法以及 [Transport.RoundTrip] 可能异步地关闭。
//
// NewRequestWithContext 返回一个适合与 [Client.Do] 或 [Transport.RoundTrip] 一起使用的 Request。
// 要创建一个用于测试 Server Handler 的请求，可以使用 [net/http/httptest.NewRequest] 函数，
// 使用 [ReadRequest]，或手动更新 Request 字段。
// 对于外发的客户端请求，上下文控制请求及其响应的整个生命周期：获取连接、发送请求和读取响应头和主体。
// 有关入站和出站请求字段之间的区别，请参阅 [Request] 类型的文档。
//
// 如果 body 的类型是 [*bytes.Buffer]、[*bytes.Reader] 或 [*strings.Reader]，
// 则返回的请求的 ContentLength 设置为其确切值（而不是 -1），GetBody 被填充（因此 307 和 308 重定向可以重放 body），
// 并且如果 ContentLength 是 0，则 Body 设置为 [NoBody]。
func NewRequestWithContext(ctx context.Context, method, url string, body io.Reader) (*Request, error) {
	// 处理 "" 方法，视为 "GET"
	if method == "" {
		// We document that "" means "GET" for Request.Method, and people have
		// relied on that from NewRequest, so keep that working.
		// We still enforce validMethod for non-empty methods.
		method = "GET"
	}

	// 验证 method 是否有效
	if !validMethod(method) {
		return nil, fmt.Errorf("net/http: invalid method %q", method)
	}
	// 验证 ctx 是否为 nil
	if ctx == nil {
		return nil, errors.New("net/http: nil Context")
	}
	// 解析 URL
	u, err := urlpkg.Parse(url)
	if err != nil {
		return nil, err
	}
	// 如果 body 不是 io.ReadCloser 且不为 nil，则将其包装为 io.NopCloser
	rc, ok := body.(io.ReadCloser)
	if !ok && body != nil {
		rc = io.NopCloser(body)
	}
	// The host's colon:port should be normalized. See Issue 14836.
	u.Host = removeEmptyPort(u.Host)
	// 创建 Request 对象
	req := &Request{
		ctx:        ctx,
		Method:     method,
		URL:        u,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(Header),
		Body:       rc,
		Host:       u.Host,
	}
	// 如果 body 不为 nil，根据其类型设置 ContentLength 和 GetBody
	if body != nil {
		switch v := body.(type) {
		case *bytes.Buffer: // 处理 *bytes.Buffer 类型的 body
			req.ContentLength = int64(v.Len())
			buf := v.Bytes()
			req.GetBody = func() (io.ReadCloser, error) {
				r := bytes.NewReader(buf)
				return io.NopCloser(r), nil
			}
		case *bytes.Reader: // 处理 *bytes.Reader 类型的 body
			req.ContentLength = int64(v.Len())
			snapshot := *v
			req.GetBody = func() (io.ReadCloser, error) {
				r := snapshot
				return io.NopCloser(&r), nil
			}
		case *strings.Reader: // 处理 *strings.Reader 类型的 body
			req.ContentLength = int64(v.Len())
			snapshot := *v
			req.GetBody = func() (io.ReadCloser, error) {
				r := snapshot
				return io.NopCloser(&r), nil
			}
		default:
			// This is where we'd set it to -1 (at least
			// if body != NoBody) to mean unknown, but
			// that broke people during the Go 1.8 testing
			// period. People depend on it being 0 I
			// guess. Maybe retry later. See Issue 18117.
		}
		// For client requests, Request.ContentLength of 0
		// means either actually 0, or unknown. The only way
		// to explicitly say that the ContentLength is zero is
		// to set the Body to nil. But turns out too much code
		// depends on NewRequest returning a non-nil Body,
		// so we use a well-known ReadCloser variable instead
		// and have the http package also treat that sentinel
		// variable to mean explicitly zero.
		if req.GetBody != nil && req.ContentLength == 0 {
			req.Body = NoBody
			req.GetBody = func() (io.ReadCloser, error) { return NoBody, nil }
		}
	}

	return req, nil
}

// BasicAuth returns the username and password provided in the request's
// Authorization header, if the request uses HTTP Basic Authentication.
// See RFC 2617, Section 2.

// BasicAuth 返回请求的 Authorization 头中提供的用户名和密码,
// 如果请求使用 HTTP Basic Authentication 的话。请参阅 RFC 2617 第 2 节。
func (r *Request) BasicAuth() (username, password string, ok bool) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", "", false
	}
	return parseBasicAuth(auth)
}

// parseBasicAuth parses an HTTP Basic Authentication string.
// "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==" returns ("Aladdin", "open sesame", true).
//
// parseBasicAuth should be an internal detail,
// but widely used packages access it using linkname.
// Notable members of the hall of shame include:
//   - github.com/sagernet/sing
//
// Do not remove or change the type signature.
// See go.dev/issue/67401.
//
//go:linkname parseBasicAuth

// parseBasicAuth 解析 HTTP Basic Authentication 字符串。
// "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==" returns ("Aladdin", "open sesame", true).
//
// parseBasicAuth 应该是一个内部细节，但广泛使用的包通过 linkname 访问它。
// 不要删除或更改类型签名。请参阅 go.dev/issue/67401。
// 注意成员包括：
//   - github.com/sagernet/sing
//
// 不要删除或更改类型签名。请参阅 go.dev/issue/67401。
//
// go:linkname parseBasicAuth
func parseBasicAuth(auth string) (username, password string, ok bool) {
	const prefix = "Basic "
	// Case insensitive prefix match. See Issue 22736.
	if len(auth) < len(prefix) || !ascii.EqualFold(auth[:len(prefix)], prefix) {
		return "", "", false
	}
	// 截取 Basic 后面的部分进行 base64 解码
	c, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return "", "", false
	}
	// 将解码后的字节转换为字符串
	cs := string(c)
	username, password, ok = strings.Cut(cs, ":")
	if !ok {
		return "", "", false
	}
	return username, password, true
}

// SetBasicAuth sets the request's Authorization header to use HTTP
// Basic Authentication with the provided username and password.
//
// With HTTP Basic Authentication the provided username and password
// are not encrypted. It should generally only be used in an HTTPS
// request.
//
// The username may not contain a colon. Some protocols may impose
// additional requirements on pre-escaping the username and
// password. For instance, when used with OAuth2, both arguments must
// be URL encoded first with [url.QueryEscape].

// SetBasicAuth 将请求的 Authorization 头设置为使用提供的用户名和密码
// 进行 HTTP Basic Authentication。
//
// 使用 HTTP Basic Authentication 时，提供的用户名和密码未加密。
// 通常应该仅在 HTTPS 请求中使用。
//
// 用户名不能包含冒号，某些协议可能对预转义用户名和密码施加额外的要求。
// 例如，当与 OAuth2 一起使用时，两个参数都必须首先使用 [url.QueryEscape] 进行 URL 编码。
func (r *Request) SetBasicAuth(username, password string) {
	r.Header.Set("Authorization", "Basic "+basicAuth(username, password))
}

// parseRequestLine parses "GET /foo HTTP/1.1" into its three parts.

// parseRequestLine 将 "GET /foo HTTP/1.1" 解析为三个部分。
func parseRequestLine(line string) (method, requestURI, proto string, ok bool) {
	// 使用 strings.Cut 来分隔 method 和其余部分
	method, rest, ok1 := strings.Cut(line, " ")
	// 使用 strings.Cut 来分隔 requestURI 和 proto
	requestURI, proto, ok2 := strings.Cut(rest, " ")
	if !ok1 || !ok2 {
		return "", "", "", false
	}
	return method, requestURI, proto, true
}

var textprotoReaderPool sync.Pool

func newTextprotoReader(br *bufio.Reader) *textproto.Reader {
	if v := textprotoReaderPool.Get(); v != nil {
		tr := v.(*textproto.Reader)
		tr.R = br
		return tr
	}
	return textproto.NewReader(br)
}

func putTextprotoReader(r *textproto.Reader) {
	r.R = nil
	textprotoReaderPool.Put(r)
}

// ReadRequest reads and parses an incoming request from b.
//
// ReadRequest is a low-level function and should only be used for
// specialized applications; most code should use the [Server] to read
// requests and handle them via the [Handler] interface. ReadRequest
// only supports HTTP/1.x requests. For HTTP/2, use golang.org/x/net/http2.
func ReadRequest(b *bufio.Reader) (*Request, error) {
	req, err := readRequest(b)
	if err != nil {
		return nil, err
	}

	delete(req.Header, "Host")
	return req, nil
}

// readRequest should be an internal detail,
// but widely used packages access it using linkname.
// Notable members of the hall of shame include:
//   - github.com/sagernet/sing
//   - github.com/v2fly/v2ray-core/v4
//   - github.com/v2fly/v2ray-core/v5
//
// Do not remove or change the type signature.
// See go.dev/issue/67401.
//
//go:linkname readRequest
func readRequest(b *bufio.Reader) (req *Request, err error) {
	tp := newTextprotoReader(b)
	defer putTextprotoReader(tp)

	req = new(Request)

	// First line: GET /index.html HTTP/1.0
	var s string
	if s, err = tp.ReadLine(); err != nil {
		return nil, err
	}
	defer func() {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
	}()

	var ok bool
	req.Method, req.RequestURI, req.Proto, ok = parseRequestLine(s)
	if !ok {
		return nil, badStringError("malformed HTTP request", s)
	}
	if !validMethod(req.Method) {
		return nil, badStringError("invalid method", req.Method)
	}
	rawurl := req.RequestURI
	if req.ProtoMajor, req.ProtoMinor, ok = ParseHTTPVersion(req.Proto); !ok {
		return nil, badStringError("malformed HTTP version", req.Proto)
	}

	// CONNECT requests are used two different ways, and neither uses a full URL:
	// The standard use is to tunnel HTTPS through an HTTP proxy.
	// It looks like "CONNECT www.google.com:443 HTTP/1.1", and the parameter is
	// just the authority section of a URL. This information should go in req.URL.Host.
	//
	// The net/rpc package also uses CONNECT, but there the parameter is a path
	// that starts with a slash. It can be parsed with the regular URL parser,
	// and the path will end up in req.URL.Path, where it needs to be in order for
	// RPC to work.
	justAuthority := req.Method == "CONNECT" && !strings.HasPrefix(rawurl, "/")
	if justAuthority {
		rawurl = "http://" + rawurl
	}

	if req.URL, err = url.ParseRequestURI(rawurl); err != nil {
		return nil, err
	}

	if justAuthority {
		// Strip the bogus "http://" back off.
		req.URL.Scheme = ""
	}

	// Subsequent lines: Key: value.
	mimeHeader, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, err
	}
	req.Header = Header(mimeHeader)
	if len(req.Header["Host"]) > 1 {
		return nil, fmt.Errorf("too many Host headers")
	}

	// RFC 7230, section 5.3: Must treat
	//	GET /index.html HTTP/1.1
	//	Host: www.google.com
	// and
	//	GET http://www.google.com/index.html HTTP/1.1
	//	Host: doesntmatter
	// the same. In the second case, any Host line is ignored.
	req.Host = req.URL.Host
	if req.Host == "" {
		req.Host = req.Header.get("Host")
	}

	fixPragmaCacheControl(req.Header)

	req.Close = shouldClose(req.ProtoMajor, req.ProtoMinor, req.Header, false)

	err = readTransfer(req, b)
	if err != nil {
		return nil, err
	}

	if req.isH2Upgrade() {
		// Because it's neither chunked, nor declared:
		req.ContentLength = -1

		// We want to give handlers a chance to hijack the
		// connection, but we need to prevent the Server from
		// dealing with the connection further if it's not
		// hijacked. Set Close to ensure that:
		req.Close = true
	}
	return req, nil
}

// MaxBytesReader is similar to [io.LimitReader] but is intended for
// limiting the size of incoming request bodies. In contrast to
// io.LimitReader, MaxBytesReader's result is a ReadCloser, returns a
// non-nil error of type [*MaxBytesError] for a Read beyond the limit,
// and closes the underlying reader when its Close method is called.
//
// MaxBytesReader prevents clients from accidentally or maliciously
// sending a large request and wasting server resources. If possible,
// it tells the [ResponseWriter] to close the connection after the limit
// has been reached.
func MaxBytesReader(w ResponseWriter, r io.ReadCloser, n int64) io.ReadCloser {
	if n < 0 { // Treat negative limits as equivalent to 0.
		n = 0
	}
	return &maxBytesReader{w: w, r: r, i: n, n: n}
}

// MaxBytesError is returned by [MaxBytesReader] when its read limit is exceeded.
type MaxBytesError struct {
	Limit int64
}

func (e *MaxBytesError) Error() string {
	// Due to Hyrum's law, this text cannot be changed.
	return "http: request body too large"
}

type maxBytesReader struct {
	w   ResponseWriter
	r   io.ReadCloser // underlying reader
	i   int64         // max bytes initially, for MaxBytesError
	n   int64         // max bytes remaining
	err error         // sticky error
}

func (l *maxBytesReader) Read(p []byte) (n int, err error) {
	if l.err != nil {
		return 0, l.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	// If they asked for a 32KB byte read but only 5 bytes are
	// remaining, no need to read 32KB. 6 bytes will answer the
	// question of the whether we hit the limit or go past it.
	// 0 < len(p) < 2^63
	if int64(len(p))-1 > l.n {
		p = p[:l.n+1]
	}
	n, err = l.r.Read(p)

	if int64(n) <= l.n {
		l.n -= int64(n)
		l.err = err
		return n, err
	}

	n = int(l.n)
	l.n = 0

	// The server code and client code both use
	// maxBytesReader. This "requestTooLarge" check is
	// only used by the server code. To prevent binaries
	// which only using the HTTP Client code (such as
	// cmd/go) from also linking in the HTTP server, don't
	// use a static type assertion to the server
	// "*response" type. Check this interface instead:
	type requestTooLarger interface {
		requestTooLarge()
	}
	if res, ok := l.w.(requestTooLarger); ok {
		res.requestTooLarge()
	}
	l.err = &MaxBytesError{l.i}
	return n, l.err
}

func (l *maxBytesReader) Close() error {
	return l.r.Close()
}

func copyValues(dst, src url.Values) {
	for k, vs := range src {
		dst[k] = append(dst[k], vs...)
	}
}

func parsePostForm(r *Request) (vs url.Values, err error) {
	if r.Body == nil {
		err = errors.New("missing form body")
		return
	}
	ct := r.Header.Get("Content-Type")
	// RFC 7231, section 3.1.1.5 - empty type
	//   MAY be treated as application/octet-stream
	if ct == "" {
		ct = "application/octet-stream"
	}
	ct, _, err = mime.ParseMediaType(ct)
	switch {
	case ct == "application/x-www-form-urlencoded":
		var reader io.Reader = r.Body
		maxFormSize := int64(1<<63 - 1)
		if _, ok := r.Body.(*maxBytesReader); !ok {
			maxFormSize = int64(10 << 20) // 10 MB is a lot of text.
			reader = io.LimitReader(r.Body, maxFormSize+1)
		}
		b, e := io.ReadAll(reader)
		if e != nil {
			if err == nil {
				err = e
			}
			break
		}
		if int64(len(b)) > maxFormSize {
			err = errors.New("http: POST too large")
			return
		}
		vs, e = url.ParseQuery(string(b))
		if err == nil {
			err = e
		}
	case ct == "multipart/form-data":
		// handled by ParseMultipartForm (which is calling us, or should be)
		// TODO(bradfitz): there are too many possible
		// orders to call too many functions here.
		// Clean this up and write more tests.
		// request_test.go contains the start of this,
		// in TestParseMultipartFormOrder and others.
	}
	return
}

// ParseForm populates r.Form and r.PostForm.
//
// For all requests, ParseForm parses the raw query from the URL and updates
// r.Form.
//
// For POST, PUT, and PATCH requests, it also reads the request body, parses it
// as a form and puts the results into both r.PostForm and r.Form. Request body
// parameters take precedence over URL query string values in r.Form.
//
// If the request Body's size has not already been limited by [MaxBytesReader],
// the size is capped at 10MB.
//
// For other HTTP methods, or when the Content-Type is not
// application/x-www-form-urlencoded, the request Body is not read, and
// r.PostForm is initialized to a non-nil, empty value.
//
// [Request.ParseMultipartForm] calls ParseForm automatically.
// ParseForm is idempotent.
func (r *Request) ParseForm() error {
	var err error
	if r.PostForm == nil {
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" {
			r.PostForm, err = parsePostForm(r)
		}
		if r.PostForm == nil {
			r.PostForm = make(url.Values)
		}
	}
	if r.Form == nil {
		if len(r.PostForm) > 0 {
			r.Form = make(url.Values)
			copyValues(r.Form, r.PostForm)
		}
		var newValues url.Values
		if r.URL != nil {
			var e error
			newValues, e = url.ParseQuery(r.URL.RawQuery)
			if err == nil {
				err = e
			}
		}
		if newValues == nil {
			newValues = make(url.Values)
		}
		if r.Form == nil {
			r.Form = newValues
		} else {
			copyValues(r.Form, newValues)
		}
	}
	return err
}

// ParseMultipartForm parses a request body as multipart/form-data.
// The whole request body is parsed and up to a total of maxMemory bytes of
// its file parts are stored in memory, with the remainder stored on
// disk in temporary files.
// ParseMultipartForm calls [Request.ParseForm] if necessary.
// If ParseForm returns an error, ParseMultipartForm returns it but also
// continues parsing the request body.
// After one call to ParseMultipartForm, subsequent calls have no effect.
func (r *Request) ParseMultipartForm(maxMemory int64) error {
	if r.MultipartForm == multipartByReader {
		return errors.New("http: multipart handled by MultipartReader")
	}
	var parseFormErr error
	if r.Form == nil {
		// Let errors in ParseForm fall through, and just
		// return it at the end.
		parseFormErr = r.ParseForm()
	}
	if r.MultipartForm != nil {
		return nil
	}

	mr, err := r.multipartReader(false)
	if err != nil {
		return err
	}

	f, err := mr.ReadForm(maxMemory)
	if err != nil {
		return err
	}

	if r.PostForm == nil {
		r.PostForm = make(url.Values)
	}
	for k, v := range f.Value {
		r.Form[k] = append(r.Form[k], v...)
		// r.PostForm should also be populated. See Issue 9305.
		r.PostForm[k] = append(r.PostForm[k], v...)
	}

	r.MultipartForm = f

	return parseFormErr
}

// FormValue returns the first value for the named component of the query.
// The precedence order:
//  1. application/x-www-form-urlencoded form body (POST, PUT, PATCH only)
//  2. query parameters (always)
//  3. multipart/form-data form body (always)
//
// FormValue calls [Request.ParseMultipartForm] and [Request.ParseForm]
// if necessary and ignores any errors returned by these functions.
// If key is not present, FormValue returns the empty string.
// To access multiple values of the same key, call ParseForm and
// then inspect [Request.Form] directly.
func (r *Request) FormValue(key string) string {
	if r.Form == nil {
		r.ParseMultipartForm(defaultMaxMemory)
	}
	if vs := r.Form[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// PostFormValue returns the first value for the named component of the POST,
// PUT, or PATCH request body. URL query parameters are ignored.
// PostFormValue calls [Request.ParseMultipartForm] and [Request.ParseForm] if necessary and ignores
// any errors returned by these functions.
// If key is not present, PostFormValue returns the empty string.
func (r *Request) PostFormValue(key string) string {
	if r.PostForm == nil {
		r.ParseMultipartForm(defaultMaxMemory)
	}
	if vs := r.PostForm[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// FormFile returns the first file for the provided form key.
// FormFile calls [Request.ParseMultipartForm] and [Request.ParseForm] if necessary.
func (r *Request) FormFile(key string) (multipart.File, *multipart.FileHeader, error) {
	if r.MultipartForm == multipartByReader {
		return nil, nil, errors.New("http: multipart handled by MultipartReader")
	}
	if r.MultipartForm == nil {
		err := r.ParseMultipartForm(defaultMaxMemory)
		if err != nil {
			return nil, nil, err
		}
	}
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		if fhs := r.MultipartForm.File[key]; len(fhs) > 0 {
			f, err := fhs[0].Open()
			return f, fhs[0], err
		}
	}
	return nil, nil, ErrMissingFile
}

// PathValue returns the value for the named path wildcard in the [ServeMux] pattern
// that matched the request.
// It returns the empty string if the request was not matched against a pattern
// or there is no such wildcard in the pattern.
func (r *Request) PathValue(name string) string {
	if i := r.patIndex(name); i >= 0 {
		return r.matches[i]
	}
	return r.otherValues[name]
}

// SetPathValue sets name to value, so that subsequent calls to r.PathValue(name)
// return value.
func (r *Request) SetPathValue(name, value string) {
	if i := r.patIndex(name); i >= 0 {
		r.matches[i] = value
	} else {
		if r.otherValues == nil {
			r.otherValues = map[string]string{}
		}
		r.otherValues[name] = value
	}
}

// patIndex returns the index of name in the list of named wildcards of the
// request's pattern, or -1 if there is no such name.
func (r *Request) patIndex(name string) int {
	// The linear search seems expensive compared to a map, but just creating the map
	// takes a lot of time, and most patterns will just have a couple of wildcards.
	if r.pat == nil {
		return -1
	}
	i := 0
	for _, seg := range r.pat.segments {
		if seg.wild && seg.s != "" {
			if name == seg.s {
				return i
			}
			i++
		}
	}
	return -1
}

func (r *Request) expectsContinue() bool {
	return hasToken(r.Header.get("Expect"), "100-continue")
}

func (r *Request) wantsHttp10KeepAlive() bool {
	if r.ProtoMajor != 1 || r.ProtoMinor != 0 {
		return false
	}
	return hasToken(r.Header.get("Connection"), "keep-alive")
}

func (r *Request) wantsClose() bool {
	if r.Close {
		return true
	}
	return hasToken(r.Header.get("Connection"), "close")
}

func (r *Request) closeBody() error {
	if r.Body == nil {
		return nil
	}
	return r.Body.Close()
}

func (r *Request) isReplayable() bool {
	if r.Body == nil || r.Body == NoBody || r.GetBody != nil {
		switch valueOrDefault(r.Method, "GET") {
		case "GET", "HEAD", "OPTIONS", "TRACE":
			return true
		}
		// The Idempotency-Key, while non-standard, is widely used to
		// mean a POST or other request is idempotent. See
		// https://golang.org/issue/19943#issuecomment-421092421
		if r.Header.has("Idempotency-Key") || r.Header.has("X-Idempotency-Key") {
			return true
		}
	}
	return false
}

// outgoingLength reports the Content-Length of this outgoing (Client) request.
// It maps 0 into -1 (unknown) when the Body is non-nil.
func (r *Request) outgoingLength() int64 {
	if r.Body == nil || r.Body == NoBody {
		return 0
	}
	if r.ContentLength != 0 {
		return r.ContentLength
	}
	return -1
}

// requestMethodUsuallyLacksBody reports whether the given request
// method is one that typically does not involve a request body.
// This is used by the Transport (via
// transferWriter.shouldSendChunkedRequestBody) to determine whether
// we try to test-read a byte from a non-nil Request.Body when
// Request.outgoingLength() returns -1. See the comments in
// shouldSendChunkedRequestBody.
func requestMethodUsuallyLacksBody(method string) bool {
	switch method {
	case "GET", "HEAD", "DELETE", "OPTIONS", "PROPFIND", "SEARCH":
		return true
	}
	return false
}

// requiresHTTP1 reports whether this request requires being sent on
// an HTTP/1 connection.
func (r *Request) requiresHTTP1() bool {
	return hasToken(r.Header.Get("Connection"), "upgrade") &&
		ascii.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
