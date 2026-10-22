import { Code, ConnectError } from "@connectrpc/connect";

// upload sends a model file to POST /api/uploads and resolves with the id
// CreateModel takes in place of the file's bytes. The browser streams the
// file from disk, so it is never read into the page's memory, and the
// server writes it to disk as it arrives. progress hears the fraction
// sent so far.
export function upload(file: Blob, progress?: (sent: number) => void): Promise<string> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/uploads");
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.responseType = "json";
    xhr.upload.onprogress = (e) => e.lengthComputable && progress?.(e.loaded / e.total);
    xhr.onload = () => {
      const body = xhr.response as { upload?: string; code?: string; message?: string } | null;
      if (xhr.status === 200 && body?.upload) return resolve(body.upload);
      reject(new ConnectError(body?.message ?? "", codes[body?.code ?? ""] ?? Code.Unknown));
    };
    xhr.onerror = () => reject(new ConnectError("", Code.Unavailable));
    xhr.send(file);
  });
}

// codes reads the code of an error the server answers in Connect's shape.
const codes: Record<string, Code> = {
  invalid_argument: Code.InvalidArgument,
  unauthenticated: Code.Unauthenticated,
  permission_denied: Code.PermissionDenied,
  failed_precondition: Code.FailedPrecondition,
  resource_exhausted: Code.ResourceExhausted,
  internal: Code.Internal,
};
