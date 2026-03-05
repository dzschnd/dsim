"use client";

import { useState } from "react";

type Node = {
  id: string;
  name: string;
  image: string;
  containerId: string;
  createdAt: string;
};

export function ListNodesButton() {
  const [message, setMessage] = useState<string>("");

  const handle = async () => {
    const baseUrl = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";
    const res = await fetch(`${baseUrl}/api/v1/nodes`);
    const data: Node[] = await res.json();
    setMessage(JSON.stringify(data) ?? "");
  };

  return (
    <div>
      <button
        type="button"
        onClick={handle}
        className="cursor-pointer border-2 border-white p-2 "
      >
        List nodes
      </button>
      {message && <div>Response: {message}</div>}
    </div>
  );
}
