package service

import (
	"fmt"
	"net/http"

	"go.uber.org/zap"
	"repospanner.org/repospanner/server/constants"
	"repospanner.org/repospanner/server/storage"
)

func (cfg *Service) serveGitDiscovery(w http.ResponseWriter, r *http.Request, perminfo permissionInfo, reqlogger *zap.SugaredLogger, reponame string, fakerefs bool) {
	// Git smart protocol handshake
	services := r.URL.Query()["service"]
	if len(services) != 1 {
		reqlogger.Infow("No service requested? Non-smart?",
			"services", services,
		)
		// TODO: Return client error code?
		http.NotFound(w, r)
		return
	}
	isrepoclient := len(r.Header[http.CanonicalHeaderKey("X-RepoClient-Version")]) == 1
	service := services[0]
	w.Header()["Content-Type"] = []string{"application/x-" + service + "-advertisement"}
	reqlogger = reqlogger.With("service", service)

	if service == "git-upload-pack" || service == "git-receive-pack" {
		read := service == "git-upload-pack"

		if !read && !cfg.checkAccess(perminfo, reponame, constants.CertPermissionWrite) {
			// Write access denied
			reqlogger.Info("Unauthorized request")
			http.NotFound(w, r)
			return
		}

		w.WriteHeader(200)

		if !isrepoclient {
			if err := sendPacket(w, []byte("# service="+service+"\n")); err != nil {
				reqlogger.Errorw("Error sending packet", "error", err)
				http.NotFound(w, r)
				return
			}
			if err := sendFlushPacket(w); err != nil {
				reqlogger.Errorw("Error sending packet", "error", err)
				http.NotFound(w, r)
				return
			}
		}

		refs := cfg.statestore.getGitRefs(reponame)
		symrefs := cfg.statestore.getSymRefs(reponame)

		if fakerefs {
			frefs, hasfrefs := cfg.statestore.fakerefs[reponame]
			reqlogger.Debugw(
				"Embedding fake refs",
				"frefs", frefs,
				"has", hasfrefs,
			)
			if hasfrefs {
				realrefs := refs
				refs = make(map[string]string)
				for refname, refval := range realrefs {
					refs[refname] = refval
				}
				for refname, refval := range frefs {
					refs[refname] = refval
				}
			}
		}

		if len(refs) == 0 {
			// Empty repo
			if service == "git-receive-pack" {
				pkt := []byte(fmt.Sprintf("%s capabilities^{}", storage.ZeroID))
				sendPacketWithExtensions(w, pkt, symrefs)
			}
			sendFlushPacket(w)
			return
		}
		sentexts := false
		for refname, refval := range refs {
			if !isValidRef(refval) {
				reqlogger.Errorw("Ref value impossible",
					"Refname", refname,
					"refval", refval,
				)
				continue
			}
			pkt := []byte(fmt.Sprintf("%s %s", refval, refname))
			var err error
			if !sentexts {
				err = sendPacketWithExtensions(w, pkt, symrefs)
				sentexts = true
			} else {
				err = sendPacket(w, pkt)
			}
			if err != nil {
				reqlogger.Errorw("Error sending packet", "error", err)
				http.NotFound(w, r)
				return
			}
		}
		for symref, target := range symrefs {
			refval, ok := refs[target]
			if !ok {
				reqlogger.Debugw("Symref requested for non-existing target",
					"symref", symref,
					"target", target,
				)
				continue
			}
			pkt := []byte(fmt.Sprintf("%s %s", refval, symref))
			var err error
			err = sendPacket(w, pkt)
			if err != nil {
				reqlogger.Errorw("Error sending packet", "error", err)
				http.NotFound(w, r)
				return
			}
		}

		if err := sendFlushPacket(w); err != nil {
			reqlogger.Errorw("Error sending packet", "error", err)
			http.NotFound(w, r)
			return
		}

		return
	}

	reqlogger.Infow("Invalid service requested",
		"service", service,
	)
	http.NotFound(w, r)
	return
}
