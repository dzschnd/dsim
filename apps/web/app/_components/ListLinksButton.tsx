"use client";

import { useState } from "react";

type Link = {
  id: string;
  nodeAId: string;
  nodeBId: string;
  networkId: string;
  networkName: string;
  createdAt: string;
};

export function ListLinksButton() {
  const [message, setMessage] = useState<string>("");

  const handle = async () => {
    const baseUrl = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";
    const res = await fetch(`${baseUrl}/api/v1/links`);
    const data: Link[] = await res.json();
    setMessage(JSON.stringify(data) ?? "");
  };

  return (
    <div>
      <button
        type="button"
        onClick={handle}
        className="cursor-pointer border-2 border-white p-2 "
      >
        List links
      </button>
      {message && <div>Response: {message}</div>}
    </div>
  );
}
