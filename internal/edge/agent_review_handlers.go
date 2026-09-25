package edge

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"net/http"
)

func addAgentReviewHandlers(h map[string]http.HandlerFunc, rv reviewsv1.ReviewsServiceClient) {
	h["requestAgentReview"] = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ExpectedSourceSHA string `json:"expected_source_sha"`
		}
		if err := decodeGUI(w, r, &body); err != nil {
			WriteError(w, 400, err)
			return
		}
		out, err := rv.RequestAgentReview(r.Context(), &reviewsv1.RequestAgentReviewRequest{RunId: chi.URLParam(r, "run_id"), ExpectedSourceSha: body.ExpectedSourceSHA})
		if err != nil {
			WriteError(w, guiStatus(err), err)
			return
		}
		writeReviewProto(w, 202, out)
	}
	h["listAgentReviewRequests"] = func(w http.ResponseWriter, r *http.Request) {
		out, err := rv.ListAgentReviewRequests(r.Context(), &reviewsv1.ListAgentReviewRequestsRequest{RunId: chi.URLParam(r, "run_id")})
		if err != nil {
			WriteError(w, guiStatus(err), err)
			return
		}
		writeReviewProto(w, 200, out)
	}
}
func writeReviewProto(w http.ResponseWriter, code int, m proto.Message) {
	raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(m)
	if err != nil {
		WriteError(w, 500, err)
		return
	}
	WriteJSON(w, code, json.RawMessage(raw))
}
