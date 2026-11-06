import type { Architecture, Model } from "../api/gen/mimir/v1/model_pb";

// What a model answers, in the words the build guide uses.
export function answerWord(arch?: Architecture, m?: Model) {
  if (m && m.labels.length === 2 && arch?.output !== "number" && arch?.output !== "text") return arch?.input === "image" ? "Photo · yes or no" : "Yes or no";
  switch (arch?.output) {
    case "number": return "A number";
    case "text": return "Writes text";
    case "boxes": return "Finds things in photos";
    case "ranked": return "A ranked list";
    default: return arch?.input === "image" || arch?.input === "photo_table" ? "Photo · pick a group" : "Pick an answer";
  }
}

// "92 of 100" right, or close for a number model.
export function rightWord(m: Model, arch?: Architecture) {
  return m.accuracy > 0 ? `${Math.round(m.accuracy * 100)} of 100${arch?.output === "number" ? " close" : ""}` : "";
}
