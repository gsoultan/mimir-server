import { ConnectError, Code } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

// The UI is served by the same server as the API.
export const transport = createConnectTransport({ baseUrl: location.origin });

// message turns any error into a sentence a person can act on. The server
// already writes its messages for people, so those pass through.
export function message(err: unknown): string {
  const e = ConnectError.from(err);
  switch (e.code) {
    case Code.Unavailable:
    case Code.Unknown:
      return navigator.onLine
        ? "Can't reach the server. Check that mimir-server is running."
        : "You're offline. Reconnect and try again.";
    case Code.ResourceExhausted:
      return "That file is too big for this server.";
    default:
      return e.rawMessage;
  }
}
