# The committed migrations inside the Atlas image, so an installation running
# the published images applies the schema of the release it pulled without
# having the source tree on the server. compose.release.yaml runs it the way
# compose.yaml runs the bare Atlas image against a mounted directory.
FROM arigaio/atlas:1.4.0
COPY database/migrations /migrations
