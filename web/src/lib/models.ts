import type { Architecture, Model } from "../api/gen/mimir/v1/model_pb";
import { t } from "../i18n";

// What a model answers, in the words the build guide uses.
export function answerWord(arch?: Architecture, m?: Model) {
  if (m && m.labels.length === 2 && arch?.output !== "number" && arch?.output !== "text") return arch?.input === "image" ? t("Photo · yes or no") : t("Yes or no");
  switch (arch?.output) {
    case "number": return t("A number");
    case "text": return t("Writes text");
    case "boxes": return t("Finds things in photos");
    case "ranked": return t("A ranked list");
    default: return arch?.input === "image" || arch?.input === "photo_table" ? t("Photo · pick a group") : t("Pick an answer");
  }
}

// "92 of 100" right, or close for a number model.
export function rightWord(m: Model, arch?: Architecture) {
  if (m.accuracy <= 0) return "";
  const n = Math.round(m.accuracy * 100);
  return arch?.output === "number" ? t("{n} of 100 close", { n }) : t("{n} of 100", { n });
}
