/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./templates/**/*.templ", "./templates/**/*.go"],
  theme: {
    extend: {
      fontFamily: {
        sans: ["system-ui", "-apple-system", "Segoe UI", "Roboto", "sans-serif"]
      }
    }
  },
  plugins: []
};
