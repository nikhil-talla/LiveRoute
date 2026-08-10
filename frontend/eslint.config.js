import typescriptEslint from "typescript-eslint";

export default typescriptEslint.config(
  {
    ignores: ["dist", "coverage", "src/generated"],
  },
  ...typescriptEslint.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}", "vite.config.ts"],
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      "@typescript-eslint/consistent-type-imports": "error",
      "@typescript-eslint/no-floating-promises": "error",
    },
  },
);
