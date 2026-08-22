# Release

`.github/workflows/release.yaml` is the only release path. It builds and
pushes the images to `ghcr.io/cloudyfolks-labs`, packages the Helm chart,
pushes it to `oci://ghcr.io/cloudyfolks-labs/charts` and makes the GitHub
release. Nothing is released from a workstation.

## Cut a release

1. Set the new version in `VERSION`, for example `v1.1.0`.
2. Run `make sync-version`. It copies `VERSION` into the chart image
   tags, the chart version, the chart appVersion and the CRD subchart
   version. `make verify-version` runs in CI and fails when they drift.
   Then run `helm-docs` in `charts/fabric` to regenerate `README.md`
   from `values.yaml`. CI does not check the chart README.
3. Commit both changes and merge them to `main`.
4. Push the tag: `git tag v1.1.0 && git push origin v1.1.0`.

The tag push starts the workflow. It refuses to publish when the chart
version does not match the tag.

## Test build

Run the workflow with `workflow_dispatch` to build and push a
`dev-<short sha>` image and a `0.0.0-dev-<short sha>` chart. This makes
no GitHub release and no tag.
