// Package web 内嵌管理台前端页面。
package web

import _ "embed"

//go:embed ui.html
var indexHTML []byte

// HTML 返回内嵌的管理页面内容。
func HTML() []byte {
	return indexHTML
}
