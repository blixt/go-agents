package web

import "net/http"

type Server struct {
	Dir string
}

func (s *Server) Handler() http.Handler {
	return http.FileServer(http.Dir(s.Dir))
}
