// Helpers for the speech and photo models: a recording made into the
// 16 kHz mono WAV the speech model listens to, and photos made small
// before they are sent.

// speechRate is the rate speech models listen at.
export const speechRate = 16000;

// wav encodes mono samples, from -1 to 1, as a 16-bit WAV file.
export function wav(samples: Float32Array, rate: number): Uint8Array {
  const out = new Uint8Array(44 + samples.length * 2);
  const v = new DataView(out.buffer);
  const text = (at: number, s: string) => { for (let i = 0; i < s.length; i++) out[at + i] = s.charCodeAt(i); };
  text(0, "RIFF");
  v.setUint32(4, 36 + samples.length * 2, true);
  text(8, "WAVE");
  text(12, "fmt ");
  v.setUint32(16, 16, true);
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // mono
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true);
  v.setUint16(32, 2, true);
  v.setUint16(34, 16, true);
  text(36, "data");
  v.setUint32(40, samples.length * 2, true);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]!));
    v.setInt16(44 + i * 2, Math.round(s < 0 ? s * 0x8000 : s * 0x7fff), true);
  }
  return out;
}

// Recording is a sound made ready for a speech model.
export type Recording = { wav: Uint8Array; seconds: number };

// toSpeech decodes a recording in any format the browser plays, mixes it
// to mono and resamples it to 16 kHz, so that a long recording sends a
// small file.
export async function toSpeech(file: Blob): Promise<Recording> {
  const ctx = new AudioContext();
  let sound: AudioBuffer;
  try {
    sound = await ctx.decodeAudioData(await file.arrayBuffer());
  } finally {
    void ctx.close();
  }
  const length = Math.max(1, Math.ceil(sound.duration * speechRate));
  const off = new OfflineAudioContext(1, length, speechRate);
  const src = off.createBufferSource();
  src.buffer = sound;
  src.connect(off.destination);
  src.start();
  const mono = await off.startRendering();
  return { wav: wav(mono.getChannelData(0), speechRate), seconds: sound.duration };
}

// photoSide is the shorter side photos are shrunk to before they are
// sent; the photo models look at a square of 224 from its middle.
export const photoSide = 448;

// shrinkPhoto returns a photo as a JPG whose shorter side is at most
// photoSide, and a link to show it by.
export async function shrinkPhoto(file: Blob): Promise<{ bytes: Uint8Array; url: string }> {
  const img = await createImageBitmap(file);
  const scale = Math.min(1, photoSide / Math.min(img.width, img.height));
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.round(img.width * scale));
  canvas.height = Math.max(1, Math.round(img.height * scale));
  const g = canvas.getContext("2d");
  if (!g) throw new Error("This browser can't read photos.");
  g.imageSmoothingQuality = "high";
  g.drawImage(img, 0, 0, canvas.width, canvas.height);
  img.close();
  const blob = await new Promise<Blob | null>((ok) => canvas.toBlob(ok, "image/jpeg", 0.92));
  if (!blob) throw new Error("This browser can't read photos.");
  return { bytes: new Uint8Array(await blob.arrayBuffer()), url: URL.createObjectURL(blob) };
}

// clock writes seconds as m:ss, or h:mm:ss from an hour.
export function clock(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
  const two = (n: number) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${two(m)}:${two(r)}` : `${m}:${two(r)}`;
}
