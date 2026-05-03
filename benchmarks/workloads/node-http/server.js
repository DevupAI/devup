const http = require("http");

const host = "0.0.0.0";
const port = Number(process.env.PORT || 3000);

const server = http.createServer((req, res) => {
  const body = JSON.stringify({
    status: "ok",
    runtime: "node",
    pid: process.pid,
  });
  res.writeHead(200, {
    "Content-Type": "application/json",
    "Content-Length": Buffer.byteLength(body),
  });
  res.end(body);
});

server.listen(port, host, () => {
  process.stdout.write("READY\n");
});
