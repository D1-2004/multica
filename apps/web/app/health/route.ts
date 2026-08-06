// Health check endpoint for multica CLI probeServer.
// The CLI (server/cmd/multica/cmd_setup.go) probes <server-url>/health
// and expects HTTP 200 to consider the server reachable.
export function GET() {
  return Response.json(
    { status: "ok" },
    {
      status: 200,
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-cache, no-store, must-revalidate",
      },
    },
  );
}
