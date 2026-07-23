import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

export default defineConfig([
  ...nextVitals,
  ...nextTs,
  globalIgnores([
    ".next/**",
    "node_modules/**",
    "coverage/**",
    "playwright-report/**",
    "test-results/**",
    "next-env.d.ts"
  ]),
  {
    files: ["**/*.{ts,tsx}"],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: "JSXExpressionContainer > MemberExpression[property.name='event_type']",
          message: "事件代码不能直接展示给用户，请使用 auditEventLabel() 转换。",
        },
        {
          selector: "JSXExpressionContainer > MemberExpression[computed=true][property.value='event_type']",
          message: "事件代码不能直接展示给用户，请使用 auditEventLabel() 转换。",
        },
      ],
    },
  },
]);
