"use client";

import { useState } from "react";

export function DeleteNodeForm() {
  const [nodeId, setNodeId] = useState<string>("");
  const [message, setMessage] = useState<string>("");

  const handle = async () => {
    const baseUrl = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";
    const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeId}`, {
      method: "DELETE",
    });
    setMessage(`Status: ${res.status}`);
  };

  return (
    <div>
      <div className="flex gap-2">
        <input
          value={nodeId}
          onChange={(e) => setNodeId(e.target.value)}
          placeholder="nodeId"
          className="border-2 border-white p-2"
        />
        <button
          type="button"
          onClick={handle}
          className="cursor-pointer border-2 border-white p-2"
        >
          Delete node
        </button>
      </div>
      {message && <div>Response: {message}</div>}
    </div>
  );
}
