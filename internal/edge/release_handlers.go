package edge

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// uploadFrame is how much of an upload body travels in one gRPC frame. It is well
// under gRPC's default 4 MiB message limit, and it is the whole of what the edge
// holds of an asset at any moment: a release asset is a build output, and reading
// the request body into memory to send it in one message is how the edge gets
// killed by somebody publishing a container image.
const uploadFrame = 256 << 10

// encodedParam reads a path segment and undoes its percent-encoding.
//
// A tag is one path segment in these routes, and a git tag may contain slashes
// (a release candidate tagged "v1.0.0/rc1" is ordinary). A client has to encode
// those to keep the tag in one segment and chi hands the segment back still
// encoded, so without this a release on such a tag would be addressable by
// nothing — the same defect the repository browser had for every agent branch
// (see refParam). Asset names get the same treatment, for the same reason.
func encodedParam(r *http.Request, key string) string {
	raw := chi.URLParam(r, key)
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

// addReleaseHandlers mounts releases: a tag published for download, and the files
// published with it. It is the one part of a Git host's surface where the platform
// serves bytes a person uploaded rather than bytes Git or CI produced, so both
// directions stream and the download is served defensively.
func addReleaseHandlers(h map[string]http.HandlerFunc, c gitv1.GitServiceClient) {
	if c == nil {
		return
	}

	h["listReleases"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListReleases(r.Context(), &gitv1.ListReleasesRequest{Repo: chi.URLParam(r, "repo")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetReleases()))
		for _, rel := range resp.GetReleases() {
			out = append(out, ReleaseJSON(rel))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"releases": out})
	}

	h["createRelease"] = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tag  string `json:"tag"`
			Name string `json:"name"`
			Body string `json:"body"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.CreateRelease(r.Context(), &gitv1.CreateReleaseRequest{
			Repo: chi.URLParam(r, "repo"), Tag: body.Tag, Name: body.Name, Body: body.Body,
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, ReleaseJSON(resp.GetRelease()))
	}

	h["deleteRelease"] = func(w http.ResponseWriter, r *http.Request) {
		_, err := c.DeleteRelease(r.Context(), &gitv1.DeleteReleaseRequest{
			Repo: chi.URLParam(r, "repo"), Tag: encodedParam(r, "tag"),
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"deleted": encodedParam(r, "tag")})
	}

	h["uploadReleaseAsset"] = func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			WriteError(w, http.StatusBadRequest, errors.New("name is required: an asset is downloaded by its file name"))
			return
		}
		defer r.Body.Close()
		stream, err := c.UploadReleaseAsset(r.Context())
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		// The first frame names the release and the asset; every frame after it is
		// body only. The body is read a frame at a time, so the edge never holds
		// more than uploadFrame bytes of it.
		meta := &gitv1.UploadReleaseAssetRequest{
			Repo: chi.URLParam(r, "repo"), Tag: encodedParam(r, "tag"),
			Name: name, ContentType: r.Header.Get("Content-Type"),
		}
		buf := make([]byte, uploadFrame)
		for {
			n, rerr := r.Body.Read(buf)
			if n > 0 || meta != nil {
				frame := &gitv1.UploadReleaseAssetRequest{Data: buf[:n]}
				if meta != nil {
					frame.Repo, frame.Tag, frame.Name, frame.ContentType =
						meta.GetRepo(), meta.GetTag(), meta.GetName(), meta.GetContentType()
					meta = nil
				}
				if serr := stream.Send(frame); serr != nil {
					// A send failure means the server has already given up, and its
					// reason is on CloseAndRecv rather than here.
					break
				}
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					WriteError(w, http.StatusBadRequest, rerr)
					return
				}
				break
			}
		}
		resp, err := stream.CloseAndRecv()
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, releaseAssetJSON(resp.GetAsset()))
	}

	h["downloadReleaseAsset"] = func(w http.ResponseWriter, r *http.Request) {
		stream, err := c.DownloadReleaseAsset(r.Context(), &gitv1.DownloadReleaseAssetRequest{
			Repo: chi.URLParam(r, "repo"), Tag: encodedParam(r, "tag"), Name: encodedParam(r, "name"),
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		// Nothing is written until the first message arrives, so an asset that does
		// not exist — or belongs to another organization — is still a 404 rather
		// than a 200 carrying an empty file.
		first, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			WriteError(w, http.StatusNotFound, errors.New("no such release asset"))
			return
		}
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		// The uploader's own content type is not echoed back: it is attacker-chosen
		// text, and serving text/html from the platform's origin because an upload
		// asked for it is stored cross-site scripting. The type is derived from the
		// file name with the same inert-type rules CI artifacts use.
		w.Header().Set("Content-Type", artifactContentType(first.GetName()))
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": first.GetName()}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Content-Length", strconv.FormatInt(first.GetSizeBytes(), 10))
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(first.GetData()); err != nil {
			return
		}
		for {
			msg, err := stream.Recv()
			if err != nil {
				// Headers are gone; a failure part-way can only cut the body short,
				// which the declared Content-Length makes detectable.
				return
			}
			if _, err := w.Write(msg.GetData()); err != nil {
				return
			}
		}
	}
}

// ReleaseJSON renders a release and its assets. It is exported so a test can pin
// the body without standing up a router: the GUI reads these field names, and a
// renamed field is a screen showing nothing.
func ReleaseJSON(rel *gitv1.Release) map[string]any {
	assets := make([]map[string]any, 0, len(rel.GetAssets()))
	for _, a := range rel.GetAssets() {
		assets = append(assets, releaseAssetJSON(a))
	}
	return map[string]any{
		"id": rel.GetId(), "repo_id": rel.GetRepoId(),
		"tag": rel.GetTag(), "name": rel.GetName(), "body": rel.GetBody(),
		"created_at": rel.GetCreatedAt(), "assets": assets,
	}
}

func releaseAssetJSON(a *gitv1.ReleaseAsset) map[string]any {
	return map[string]any{
		"id": a.GetId(), "name": a.GetName(),
		"size_bytes": a.GetSizeBytes(), "content_type": a.GetContentType(),
	}
}
