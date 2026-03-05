"use client";

import { useState } from "react";

type Response = {
  message: string;
};

export function CreateButton() {
  const [message, setMessage] = useState<string>("");

  const handle = async () => {
    const baseUrl = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";
    const res = await fetch(`${baseUrl}/api/v1/nodes`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    });
    const data: Response = await res.json();
    setMessage(JSON.stringify(data) ?? "");
  };

  return (
    <div>
      <button
        type="button"
        onClick={handle}
        className="cursor-pointer border-2 border-white p-2 "
      >
        Create node
      </button>
      {message && <div>Response: {message}</div>}
    </div>
  );
}
