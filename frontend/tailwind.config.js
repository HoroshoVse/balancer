/** @type {import('tailwindcss').Config} */
export default {
  darkMode: ["class"],
  content: [
    './pages/**/*.{ts,tsx}',
    './components/**/*.{ts,tsx}',
    './app/**/*.{ts,tsx}',
    './src/**/*.{ts,tsx}',
  ],
  theme: {
    container: {
      center: true,
      padding: "2rem",
      screens: {
        "2xl": "1400px",
      },
    },
    extend: {
      fontFamily: {
        sans: ['"Golos Text"', "system-ui", "sans-serif"],
        display: ["Unbounded", '"Golos Text"', "sans-serif"],
        mono: ['"JetBrains Mono"', "ui-monospace", "monospace"],
      },
      colors: {
        border: "hsl(var(--border))",
        input: "hsl(var(--input))",
        ring: "hsl(var(--ring))",
        background: "hsl(var(--background))",
        foreground: "hsl(var(--foreground))",
        primary: {
          DEFAULT: "hsl(var(--primary))",
          foreground: "hsl(var(--primary-foreground))",
        },
        secondary: {
          DEFAULT: "hsl(var(--secondary))",
          foreground: "hsl(var(--secondary-foreground))",
        },
        destructive: {
          DEFAULT: "hsl(var(--destructive))",
          foreground: "hsl(var(--destructive-foreground))",
        },
        muted: {
          DEFAULT: "hsl(var(--muted))",
          foreground: "hsl(var(--muted-foreground))",
        },
        accent: {
          DEFAULT: "hsl(var(--accent))",
          foreground: "hsl(var(--accent-foreground))",
        },
        popover: {
          DEFAULT: "hsl(var(--popover))",
          foreground: "hsl(var(--popover-foreground))",
        },
        card: {
          DEFAULT: "hsl(var(--card))",
          foreground: "hsl(var(--card-foreground))",
        },
        // "Signal" palette
        ground: "#EEF1F4",
        ink: { DEFAULT: "#0D1117", 2: "#4A5361", 3: "#5F6877", 4: "#2B3340" },
        line: { DEFAULT: "#D9DEE5", soft: "#E6EAEF", strong: "#C4CBD4" },
        cobalt: { DEFAULT: "#2E4BFF", dark: "#1F35C7", soft: "#E7EBFF", light: "#8EA2FF", deep: "#1A2147" },
        ok: { DEFAULT: "#0E7A5A", bright: "#2ED69A", soft: "#E3F3EC", text: "#0B6B4F", pale: "#9FE8C8" },
        warn: { DEFAULT: "#D99A0B", deep: "#8A5A00", soft: "#FBF0D9", text: "#5C3D00" },
        down: { DEFAULT: "#C2410C", soft: "#FDE8DD", text: "#8F2E07" },
        rail: { DEFAULT: "#0B0F14", 2: "#11171F", 3: "#151B23", 4: "#1A212B", line: "#1F2832", text: "#E6EBF1", muted: "#8A94A3", soft: "#B4BDCA" },
      },
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
      },
      keyframes: {
        flow: { from: { transform: "translateX(-28px)" }, to: { transform: "translateX(0)" } },
        led: {
          "0%, 100%": { boxShadow: "0 0 0 3px rgba(46,214,154,.18)" },
          "50%": { boxShadow: "0 0 0 6px rgba(46,214,154,.05)" },
        },
      },
      animation: {
        flow: "flow 0.9s linear infinite",
        "flow-slow": "flow 2.4s linear infinite",
        led: "led 2.4s ease-in-out infinite",
      },
    },
  },
  plugins: [require("tailwindcss-animate")],
}
