export type ApiInterface = {
	id: string;
	name: string;
	linkId: string;
	ipAddress: string;
	prefixLength: number;
};

export type ApiNode = {
	id: string;
	name: string;
	status: string;
	type: string;
	containerId: string;
	createdAt: string;
	interfaces: ApiInterface[];
};

export type ApiLink = {
	id: string;
	interfaceAId: string;
	interfaceBId: string;
	networkId: string;
	networkName: string;
	createdAt: string;
};

type ApiError = {
	error: string;
};

export type ApiCommandResponse = {
	command: string;
	stdout: string;
	stderr: string;
	exitCode: number;
};

export async function parseApiError(res: Response): Promise<string> {
	const text = await res.text();
	if (!text) {
		return `${res.status} ${res.statusText}`;
	}

	try {
		const parsed = JSON.parse(text) as ApiError;
		if (parsed.error) {
			return `${res.status}: ${parsed.error}`;
		}
		return `${res.status} ${res.statusText}`;
	} catch {
		return `${res.status} ${res.statusText}`;
	}
}

export async function fetchTopology(baseUrl: string): Promise<{
	nodes: ApiNode[];
	links: ApiLink[];
}> {
	const [nodesRes, linksRes] = await Promise.all([
		fetch(`${baseUrl}/api/v1/nodes`),
		fetch(`${baseUrl}/api/v1/links`),
	]);

	if (!nodesRes.ok) {
		throw new Error(await parseApiError(nodesRes));
	}
	if (!linksRes.ok) {
		throw new Error(await parseApiError(linksRes));
	}

	return {
		nodes: (await nodesRes.json()) as ApiNode[],
		links: (await linksRes.json()) as ApiLink[],
	};
}

export async function createHostNode(baseUrl: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/nodes`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ type: "host" }),
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function deleteNode(baseUrl: string, nodeID: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}`, {
		method: "DELETE",
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function startNode(baseUrl: string, nodeID: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}/start`, {
		method: "POST",
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function stopNode(baseUrl: string, nodeID: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}/stop`, {
		method: "POST",
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function createLink(
	baseUrl: string,
	interfaceAID: string,
	interfaceBID: string,
): Promise<ApiLink> {
	const res = await fetch(`${baseUrl}/api/v1/links`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ interfaceAId: interfaceAID, interfaceBId: interfaceBID }),
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}

	return (await res.json()) as ApiLink;
}

export async function deleteLink(baseUrl: string, linkID: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/links/${linkID}`, {
		method: "DELETE",
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function runNodeCommand(
	baseUrl: string,
	nodeID: string,
	command: string,
): Promise<ApiCommandResponse> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}/cli`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ command }),
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}

	return (await res.json()) as ApiCommandResponse;
}
