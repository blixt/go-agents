package web

import "net/http"

type Server struct {
	Dir string
}

func (s *Server) Handler() http.Handler {
	fs := http.FileServer(http.Dir(s.Dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		fs.ServeHTTP(w, r)
	})
}
