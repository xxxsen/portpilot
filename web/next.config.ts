import type { NextConfig } from "next";

const adminApi = process.env.PORTPILOT_ADMIN_API ?? "http://127.0.0.1:5100";

const nextConfig: NextConfig = {
  output: "standalone",
  async rewrites() {
    return [
      {
        source: "/api/:path*",
        destination: `${adminApi}/api/:path*`,
      },
    ];
  },
};

export default nextConfig;
