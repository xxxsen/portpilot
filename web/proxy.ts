import {NextRequest, NextResponse} from "next/server";

export function proxy(request: NextRequest) {
  if (process.env.NODE_ENV !== "development") {
    return NextResponse.next();
  }
  const headers = new Headers(request.headers);
  headers.delete("origin");
  return NextResponse.next({request: {headers}});
}

export const config = {
  matcher: ["/api/knock/requests", "/api/knock/verify"],
};
