package execworker

import (
	"encoding/json"
	"net/http"
)

func (w *Worker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/dispatch", w.handleDispatch)
	return mux
}

func (w *Worker) handleDispatch(wr http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		wr.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var task Task
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		wr.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := w.RunTask(r.Context(), task); err != nil {
		wr.WriteHeader(http.StatusInternalServerError)
		return
	}
	wr.WriteHeader(http.StatusOK)
}
