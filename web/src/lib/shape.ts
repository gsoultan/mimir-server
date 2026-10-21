// The shape of a table model: its hidden steps, as someone designs them,
// and what a shape costs in numbers learned and examples wanted.

export type Step = { kind: "dense" | "dropout"; units: number; rate: number };
export type Size = "small" | "medium" | "large" | "design";

const dense = (units: number): Step => ({ kind: "dense", units, rate: 0 });
const dropout = (rate: number): Step => ({ kind: "dropout", units: 0, rate });

// medium is the server's usual shape for a row of `inputs` numbers.
export function medium(inputs: number): Step[] {
  const hidden = Math.max(16, Math.min(256, 4 * inputs));
  return [dense(hidden), dense(hidden)];
}

export const sizes: Record<Exclude<Size, "design">, (inputs: number) => Step[]> = {
  small: (inputs) => [dense(Math.max(8, Math.min(64, inputs)))],
  medium,
  large: (inputs) => {
    const hidden = Math.max(64, Math.min(512, 8 * inputs));
    return [dense(hidden), dropout(0.1), dense(hidden), dropout(0.1), dense(Math.max(32, Math.round(hidden / 2)))];
  },
};

// layersOf is the design to send when teaching; Medium sends none, so the
// server keeps its usual shape.
export function layersOf(size: Size, steps: Step[], inputs: number): Step[] {
  return size === "medium" ? [] : size === "design" ? steps : sizes[size](inputs);
}

// learned counts the weights and biases of a shape from inputs numbers to
// outputs answers.
export function learned(inputs: number, steps: Step[], outputs: number): number {
  let n = 0;
  let width = inputs;
  for (const s of steps) {
    if (s.kind !== "dense") continue;
    n += width * s.units + s.units;
    width = s.units;
  }
  return n + width * outputs + outputs;
}

// examplesWanted is a rule of thumb: about one example per two numbers
// learned, at least 200, rounded to a friendly figure.
export function examplesWanted(params: number): number {
  const raw = Math.max(200, params / 2);
  const step = raw >= 10_000 ? 5_000 : raw >= 1_000 ? 500 : 100;
  return Math.ceil(raw / step) * step;
}
