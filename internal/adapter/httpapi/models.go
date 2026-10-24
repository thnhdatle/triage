// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"net/http"
	"slices"
)

const modelObject = "model"

type wireModelList struct {
	Object string      `json:"object"`
	Data   []wireModel `json:"data"`
}

type wireModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	created := s.started.Unix()
	out := wireModelList{Object: "list", Data: []wireModel{}}
	for _, name := range slices.Sorted(slices.Values(s.catalog.Routes())) {
		out.Data = append(out.Data, wireModel{ID: name, Object: modelObject, Created: created, OwnedBy: "triage"})
	}
	if s.current().ExposeTargets {
		for _, t := range s.catalog.Targets() {
			out.Data = append(out.Data, wireModel{ID: t.Name, Object: modelObject, Created: created, OwnedBy: t.Provider})
		}
	}
	writeJSON(w, http.StatusOK, out)
}
