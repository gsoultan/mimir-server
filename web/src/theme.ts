import { createTheme, type MantineColorsTuple } from "@mantine/core";

const teal: MantineColorsTuple = [
  "#E2F1EC", "#C6E4DA", "#A0D2C2", "#76BDA8", "#4FA78E",
  "#2E9178", "#1B7D67", "#0E6B5C", "#0A5246", "#063A31",
];

export const theme = createTheme({
  primaryColor: "teal",
  primaryShade: 7,
  colors: { teal },
  fontFamily: "'Figtree Variable', system-ui, sans-serif",
  headings: { fontFamily: "'Bricolage Grotesque Variable', 'Figtree Variable', sans-serif", fontWeight: "700" },
  defaultRadius: "md",
  components: {
    Button: { defaultProps: { size: "md" } },
  },
});
