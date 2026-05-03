require "json"
require "socket"

host = "0.0.0.0"
port = Integer(ENV.fetch("PORT", "4567"))

server = TCPServer.new(host, port)
STDOUT.sync = true
puts "READY"

loop do
  client = server.accept
  begin
    client.gets
    body = JSON.generate({
      status: "ok",
      runtime: "ruby",
      pid: Process.pid,
    })
    client.write("HTTP/1.1 200 OK\r\n")
    client.write("Content-Type: application/json\r\n")
    client.write("Content-Length: #{body.bytesize}\r\n")
    client.write("\r\n")
    client.write(body)
  rescue StandardError
    # Ignore malformed requests in the benchmark harness.
  ensure
    client.close
  end
end
