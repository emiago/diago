# JSSIP Example

This example demonstrates a WebRTC-based SIP client using [JsSIP](https://github.com/versatica/JsSIP).

## Setup

### 1. Backend

Start the WebRTC example server:

```bash
go run main.go
```

This launches:

* A SIP WebSocket server on port `8080` (default)
* An HTTP server for serving the web interface on port `8081` (default)

#### Command Line Options

You can customize the server using the following options:

* `-l`: SIP listen address (default: `0.0.0.0:8080`)
* `-http`: HTTP server listen address (default: `0.0.0.0:8081`)
* `-web-path`: Path prefix for static content (default: `/`)

**Example with custom options:**

```bash
go run main.go -l 127.0.0.1:8080 -http 127.0.0.1:8081 -web-path /
```

### 2. Frontend (Softphone)

The web interface is served automatically by the built-in HTTP server. Open your browser and navigate to:

```
http://localhost:8081/
```

### Configuring the Softphone

1. Open the web interface at [http://localhost:8081/](http://localhost:8081/)
2. Configure the SIP connection:

   * **Server**: `127.0.0.1` (or your server’s IP address)
   * **SIP Port**: `5060` (default)
   * **WebSocket Port**: `8080` (should match the `-l` option used above)
3. Enter your SIP account details:

   * **User**: Your extension number
   * **Password**: Your SIP password
4. Click **Login** to register with the SIP server
5. Use the dial pad to place calls

Once connected, you should hear a demo audio file when dialing.

### Making a Call

1. After logging in (REGISTER), enter a number and press **Call**
2. Allow microphone access when prompted
3. You should hear audio once the call connects
