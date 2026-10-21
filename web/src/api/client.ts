import { ConnectError, Code } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

// The UI is served by the same server as the API. Binary keeps uploads of
// tables and photo ZIPs the size of the file, not a third bigger.
export const transport = createConnectTransport({ baseUrl: location.origin, useBinaryFormat: true });

// message turns any error into a sentence a person can act on. The server
// already writes its messages for people, so those pass through.
export function message(err: unknown): string {
  const e = ConnectError.from(err);
  // The server says why it can't answer right now, such as no worker for a
  // model, in a sentence; a dropped connection has no such words.
  if (e.code === Code.Unavailable && /^[A-Z].*\.$/.test(e.rawMessage)) return e.rawMessage;
  switch (e.code) {
    case Code.Unavailable:
    case Code.Unknown:
      return navigator.onLine
        ? "Can't reach the server. Check that mimir-server is running."
        : "You're offline. Reconnect and try again.";
    case Code.ResourceExhausted:
      // The server's own limits explain themselves; a bare size refusal
      // from the transport does not.
      return /message|larger than|max/i.test(e.rawMessage) || e.rawMessage === ""
        ? "That file is too big for this server."
        : e.rawMessage;
    default:
      return e.rawMessage;
  }
}
