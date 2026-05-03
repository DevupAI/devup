<?php

$server = @stream_socket_server('tcp://0.0.0.0:8080', $errno, $errstr);
if ($server === false) {
    fwrite(STDERR, "bind failed: $errstr\n");
    exit(1);
}

echo "READY\n";
fflush(STDOUT);

while ($conn = @stream_socket_accept($server, -1)) {
    $line = fgets($conn);
    if ($line === false) {
        fclose($conn);
        continue;
    }

    while (($header = fgets($conn)) !== false) {
        if (trim($header) === '') {
            break;
        }
    }

    $body = "ok\n";
    if (str_contains($line, '/health')) {
        $body = "{\"status\":\"ok\"}\n";
    }

    fwrite($conn, "HTTP/1.1 200 OK\r\n");
    fwrite($conn, "Content-Type: text/plain\r\n");
    fwrite($conn, "Content-Length: " . strlen($body) . "\r\n");
    fwrite($conn, "Connection: close\r\n\r\n");
    fwrite($conn, $body);
    fclose($conn);
}
