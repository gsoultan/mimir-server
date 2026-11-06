import { createTheme, type CSSVariablesResolver, type MantineColorsTuple } from "@mantine/core";

// The brand teal. Shade 7 (#0E6B5C) is the primary in light mode; dark mode
// uses a lighter shade so it reads on a dark page.
const teal: MantineColorsTuple = [
  "#E2F1EC", "#C6E4DA", "#A0D2C2", "#76BDA8", "#4FA78E",
  "#2E9178", "#1B7D67", "#0E6B5C", "#0A5246", "#063A31",
];

// Warm greys with a hint of green, so teal feels at home on them.
const gray: MantineColorsTuple = [
  "#F6F6F2", "#EFEFE9", "#E3E4DD", "#D2D4CB", "#ADB3AD",
  "#7A8580", "#4B5752", "#3A4541", "#25302C", "#15201C",
];

// Dark mode surfaces, from text (0) to the page itself (7) and below.
const dark: MantineColorsTuple = [
  "#E8EEEB", "#AEB9B4", "#7F8B86", "#4A5853", "#33403B",
  "#25302C", "#1C2622", "#161E1B", "#0F1513", "#0A0F0D",
];

// "Learning" and other work in progress.
const violet: MantineColorsTuple = [
  "#F0EBFB", "#E0D5F7", "#C5B2F0", "#A98DE8", "#8F6CE0",
  "#7C55D9", "#6941C6", "#5634A6", "#432886", "#311D66",
];

export const theme = createTheme({
  primaryColor: "teal",
  primaryShade: { light: 7, dark: 4 },
  colors: { teal, gray, dark, violet },
  fontFamily: "'Figtree Variable', system-ui, sans-serif",
  fontFamilyMonospace: "ui-monospace, 'SFMono-Regular', Menlo, Consolas, monospace",
  headings: {
    fontFamily: "'Bricolage Grotesque Variable', 'Figtree Variable', sans-serif",
    fontWeight: "700",
    sizes: {
      h1: { fontSize: "1.875rem", lineHeight: "1.15" },
      h2: { fontSize: "1.125rem", lineHeight: "1.3" },
      h3: { fontSize: "1rem", lineHeight: "1.35" },
    },
  },
  radius: { xs: "4px", sm: "6px", md: "8px", lg: "12px", xl: "16px" },
  defaultRadius: "md",
  cursorType: "pointer",
  autoContrast: true,
  focusRing: "auto",
  components: {
    Button: { defaultProps: { size: "md" }, styles: { root: { fontWeight: 650 } } },
    Card: { defaultProps: { radius: "lg" }, classNames: { root: "m-card" } },
    Paper: { defaultProps: { radius: "lg" } },
    Badge: { defaultProps: { variant: "light", radius: "xl" }, styles: { root: { textTransform: "none", fontWeight: 650, letterSpacing: 0 } } },
    Modal: { defaultProps: { centered: true, radius: "lg", overlayProps: { backgroundOpacity: 0.4, blur: 2 } } },
    Drawer: { defaultProps: { overlayProps: { backgroundOpacity: 0.4, blur: 2 } } },
    Menu: { defaultProps: { radius: "md", shadow: "md" } },
    Tooltip: { defaultProps: { radius: "md" } },
    Table: { defaultProps: { verticalSpacing: "sm", horizontalSpacing: "md" } },
    Tabs: { classNames: { list: "m-tabs-list", tab: "m-tab" } },
    Alert: { defaultProps: { radius: "lg", variant: "light" } },
    Progress: { defaultProps: { radius: "xl" } },
    TextInput: { defaultProps: { radius: "md" } },
    Notification: { defaultProps: { radius: "lg" } },
  },
});

// Page and line colors for each scheme, shared with the hand-written CSS
// in styles.css through --mimir-* variables.
export const resolver: CSSVariablesResolver = () => ({
  variables: {},
  light: {
    "--mantine-color-body": "#F6F6F2",
    "--mantine-color-text": "#15201C",
    "--mantine-color-dimmed": "#5B6763",
    "--mantine-color-default": "#FFFFFF",
    "--mantine-color-default-border": "#E3E4DD",
    "--mantine-color-default-hover": "#EFEFE9",
    "--mimir-surface": "#FFFFFF",
    "--mimir-sunken": "#EFEFE9",
    "--mimir-line": "#E3E4DD",
    "--mimir-faint": "#7A8580",
    "--mimir-shadow": "0 1px 2px rgba(21,32,28,.05), 0 1px 1px rgba(21,32,28,.03)",
  },
  dark: {
    "--mantine-color-body": "#0F1513",
    "--mantine-color-text": "#E8EEEB",
    "--mantine-color-dimmed": "#AEB9B4",
    "--mantine-color-default": "#161E1B",
    "--mantine-color-default-border": "#25302C",
    "--mantine-color-default-hover": "#1C2622",
    "--mimir-surface": "#161E1B",
    "--mimir-sunken": "#1C2622",
    "--mimir-line": "#25302C",
    "--mimir-faint": "#7F8B86",
    "--mimir-shadow": "none",
  },
});
