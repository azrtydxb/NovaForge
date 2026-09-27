# Dependencies in isolated workspaces

Agent and semantic workspaces deny external network traffic. Resolve dependencies
in an operator-controlled connected environment, review the dependency changes,
and transfer immutable inputs into the repository or an approved image.

For Go, pin `go.mod` and `go.sum`, run `go mod vendor` in that connected environment,
review and commit `vendor/`, then build in the workspace with:

```sh
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go build -mod=vendor ./...
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go test -mod=vendor ./...
```

Use the same compatible Go toolchain when preparing and building. Do not rely on
the workstation module cache being present in the run. Private module credentials
belong only in the dependency preparation environment; never commit them or put
them in the runtime image. Local replacements must be transferred too.

For npm/Python and large dependency sets, an operator can prepare a platform- and
architecture-specific image containing reviewed dependency inputs and tools,
record the lockfiles/checksums and pin its digest in the appropriate supported
operator configuration. Installation hooks execute only in an isolated build
stage. A cache is not proof of dependency completeness: test a fresh workspace
with outbound network denied. The agent runtime currently uses its fixed workspace
image; repository configuration cannot substitute a custom image. Semantic producer
images have their separate operator configuration and immutable digest requirement.

`TestVendoredDependencyBuildInIsolatedWorkspace` transfers a real vendored
`github.com/google/uuid v1.6.0`, builds and executes it in the production workspace,
and confirms a TCP destination reachable from the test host is blocked inside.
Run with `source hack/env.sh` and `go test ./internal/workspace -run TestVendoredDependencyBuildInIsolatedWorkspace -count=1 -v`.
No platform credential or host filesystem mount is provided to that workspace.
