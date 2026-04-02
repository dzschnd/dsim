"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import ReactFlow, {
	Background,
	Connection,
	ConnectionLineType,
	Controls,
	Edge,
	MiniMap,
	Node,
	OnConnect,
	OnEdgesChange,
	OnNodesChange,
	applyEdgeChanges,
	applyNodeChanges,
} from "reactflow";
import "reactflow/dist/style.css";

import { SquareNode, type SquareNodeData } from "./SquareNode";
import {
	type ApiCommandResponse,
	type ApiInterface,
	type ApiLink,
	createHostNode,
	createLink,
	deleteLink,
	deleteNode,
	fetchTopology,
	runNodeCommand,
	startNode,
	stopNode,
} from "../services/topology";

type TerminalState = {
	isOpen: boolean;
	input: string;
	lines: string[];
};

type PendingConnection = {
	sourceNodeID: string;
	targetNodeID: string;
	sourceInterfaceID: string;
	targetInterfaceID: string;
};

const EDGE_STYLE = {
	stroke: "#334155",
	strokeWidth: 3,
};

function randomPos(index: number) {
	const row = Math.floor(index / 5);
	const col = index % 5;
	return {
		x: 80 + col * 220,
		y: 100 + row * 180,
	};
}

function applySelectedNode(
	nodes: Node<SquareNodeData>[],
	selectedNodeId: string,
): Node<SquareNodeData>[] {
	return nodes.map((node) => ({
		...node,
		selected: node.id === selectedNodeId,
		data: {
			...node.data,
			isSelected: node.id === selectedNodeId,
		},
	}));
}

function getAvailableInterfaces(interfaces: ApiInterface[]): ApiInterface[] {
	return interfaces.filter((iface) => iface.linkId === "");
}

function findNodeIDByInterfaceID(nodes: Node<SquareNodeData>[], interfaceID: string): string {
	const node = nodes.find((candidate) =>
		candidate.data.interfaces.some((iface) => iface.id === interfaceID),
	);
	return node?.id ?? "";
}

const nodeTypes = {
	square: SquareNode,
};

export function TopologyCanvas() {
	const baseUrl = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";

	const [nodes, setNodes] = useState<Node<SquareNodeData>[]>([]);
	const [edges, setEdges] = useState<Edge[]>([]);
	const [selectedNodeId, setSelectedNodeId] = useState<string>("");
	const [pendingConnection, setPendingConnection] = useState<PendingConnection | null>(null);
	const [busy, setBusy] = useState<boolean>(false);
	const [status, setStatus] = useState<string>("");
	const selectedNodeIdRef = useRef<string>("");
	const nodePositionsRef = useRef<Map<string, { x: number; y: number }>>(
		new Map(),
	);
	const nodesRef = useRef<Node<SquareNodeData>[]>([]);
	const terminalStateRef = useRef<Map<string, TerminalState>>(new Map());
	const toggleNodeRunRef = useRef<(nodeID: string) => Promise<void>>(async () => { });
	const toggleTerminalRef = useRef<(nodeID: string) => void>(() => { });
	const updateTerminalInputRef = useRef<(nodeID: string, value: string) => void>(() => { });
	const submitTerminalInputRef = useRef<(nodeID: string) => Promise<void>>(async () => { });

	const edgeByPair = useMemo(() => {
		const map = new Map<string, Edge>();
		for (const edge of edges) {
			const a = String(edge.source);
			const b = String(edge.target);
			const key = a < b ? `${a}|${b}` : `${b}|${a}`;
			map.set(key, edge);
		}
		return map;
	}, [edges]);

	const loadTopology = useCallback(async () => {
		setBusy(true);
		setStatus("Loading topology...");
		try {
			const { nodes: apiNodes, links: apiLinks } = await fetchTopology(baseUrl);
			const existingPositions = nodePositionsRef.current;
			const currentSelectedNodeId = selectedNodeIdRef.current;
			const terminalState = terminalStateRef.current;

			const flowNodes: Node<SquareNodeData>[] = apiNodes.map((node, index) => ({
				id: node.id,
				type: "square",
				position: existingPositions.get(node.id) ?? randomPos(index),
				selected: node.id === currentSelectedNodeId,
				data: {
					label: `${node.name} (${node.id})`,
					status: node.status,
					type: node.type,
					containerId: node.containerId,
					interfaces: node.interfaces,
					isSelected: node.id === currentSelectedNodeId,
					isBusy: false,
					isTerminalOpen:
						node.status === "running" ? (terminalState.get(node.id)?.isOpen ?? false) : false,
					terminalInput: terminalState.get(node.id)?.input ?? "",
					terminalLines: terminalState.get(node.id)?.lines ?? [],
					onToggleRun: () => void toggleNodeRunRef.current(node.id),
					onToggleTerminal: () => toggleTerminalRef.current(node.id),
					onTerminalInputChange: (value: string) => updateTerminalInputRef.current(node.id, value),
					onTerminalSubmit: () => submitTerminalInputRef.current(node.id),
				},
			}));

			const flowEdges: Edge[] = apiLinks.map((link) => ({
				id: link.id,
				type: "straight",
				source: findNodeIDByInterfaceID(flowNodes, link.interfaceAId),
				target: findNodeIDByInterfaceID(flowNodes, link.interfaceBId),
				style: EDGE_STYLE,
				data: {
					linkId: link.id,
					networkId: link.networkId,
					networkName: link.networkName,
					interfaceAId: link.interfaceAId,
					interfaceBId: link.interfaceBId,
				},
			}));

			setNodes(applySelectedNode(flowNodes, currentSelectedNodeId));
			setEdges(flowEdges);
			setStatus(`Loaded ${flowNodes.length} nodes, ${flowEdges.length} links`);
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to load topolgy");
		} finally {
			setBusy(false);
		}
	}, [baseUrl]);

	const toggleNodeRun = useCallback(
		async (nodeID: string) => {
			const currentNode = nodesRef.current.find((node) => node.id === nodeID);
			if (!currentNode) {
				setStatus("Node not found in canvas");
				return;
			}

			const action = currentNode.data.status === "running" ? "stop" : "start";

			setBusy(true);
			setStatus(`${action === "start" ? "Starting" : "Stopping"} node ${nodeID}...`);
			try {
				if (action === "start") {
					await startNode(baseUrl, nodeID);
				} else {
					await stopNode(baseUrl, nodeID);
				}
				try {
					await loadTopology();
					setStatus(`Node ${nodeID} ${action === "start" ? "started" : "stopped"}`);
				} catch (err: unknown) {
					const message = err instanceof Error ? err.message : String(err);
					setStatus(
						`Node ${nodeID} ${action === "start" ? "started" : "stopped"}, but topology reload failed: ${message}`,
					);
				}
			} catch (err: unknown) {
				const message = err instanceof Error ? err.message : String(err);
				setStatus(`Failed to ${action} node: ${message}`);
			} finally {
				setBusy(false);
			}
		},
		[baseUrl, loadTopology],
	);

	useEffect(() => {
		void loadTopology();
	}, [loadTopology]);

	useEffect(() => {
		nodePositionsRef.current = new Map(
			nodes.map((node) => [
				node.id,
				{
					x: node.position.x,
					y: node.position.y,
				},
			]),
		);
		nodesRef.current = nodes;
		terminalStateRef.current = new Map(
			nodes.map((node) => [
				node.id,
				{
					isOpen: node.data.isTerminalOpen,
					input: node.data.terminalInput,
					lines: node.data.terminalLines,
				},
			]),
		);
	}, [nodes]);

	useEffect(() => {
		setNodes((curr) =>
			curr.map((node) => ({
				...node,
				data: {
					...node.data,
					isBusy: busy,
				},
			})),
		);
	}, [busy]);

	useEffect(() => {
		toggleNodeRunRef.current = toggleNodeRun;
	}, [toggleNodeRun]);

	const toggleTerminal = useCallback((nodeID: string) => {
		setNodes((curr) =>
			curr.map((node) =>
				node.id === nodeID
					? {
						...node,
						data: {
							...node.data,
							isTerminalOpen: node.data.status === "running" ? !node.data.isTerminalOpen : false,
						},
					}
					: node,
			),
		);
	}, []);

	const updateTerminalInput = useCallback((nodeID: string, value: string) => {
		setNodes((curr) =>
			curr.map((node) =>
				node.id === nodeID
					? {
						...node,
						data: {
							...node.data,
							terminalInput: value,
						},
					}
					: node,
			),
		);
	}, []);

	const submitTerminalInput = useCallback(
		async (nodeID: string) => {
			const currentNode = nodesRef.current.find((node) => node.id === nodeID);
			if (!currentNode) {
				setStatus("Node not found in canvas");
				return;
			}

			const command = currentNode.data.terminalInput.trim();
			if (command === "") {
				return;
			}

			if (command === "clear") {
				setNodes((curr) =>
					curr.map((node) =>
						node.id === nodeID
							? {
									...node,
									data: {
										...node.data,
										terminalInput: "",
										terminalLines: [],
									},
							  }
							: node,
					),
				);
				return;
			}

			setNodes((curr) =>
				curr.map((node) =>
					node.id === nodeID
						? {
								...node,
								data: {
									...node.data,
									terminalInput: "",
								},
						  }
						: node,
				),
			);

			setBusy(true);
			try {
				const result: ApiCommandResponse = await runNodeCommand(baseUrl, nodeID, command);
				const outputLines = [
					...result.stdout
						.split("\n")
						.map((line) => line.trimEnd())
						.filter((line) => line !== ""),
					...result.stderr
						.split("\n")
						.map((line) => line.trimEnd())
						.filter((line) => line !== ""),
				];

				setNodes((curr) =>
					curr.map((node) =>
						node.id === nodeID
							? {
									...node,
									data: {
										...node.data,
										terminalLines: [...node.data.terminalLines, `$ ${command}`, ...outputLines],
									},
							  }
							: node,
					),
				);
				setStatus(`Executed ${result.command} on ${nodeID}`);
			} catch (err: unknown) {
				const message = err instanceof Error ? err.message : String(err);
				setNodes((curr) =>
					curr.map((node) =>
						node.id === nodeID
							? {
									...node,
									data: {
										...node.data,
										terminalLines: [...node.data.terminalLines, `$ ${command}`, message],
									},
							  }
							: node,
					),
				);
				setStatus(`Failed to execute command: ${message}`);
			} finally {
				setBusy(false);
			}
		},
		[baseUrl],
	);

	useEffect(() => {
		toggleTerminalRef.current = toggleTerminal;
	}, [toggleTerminal]);

	useEffect(() => {
		updateTerminalInputRef.current = updateTerminalInput;
	}, [updateTerminalInput]);

	useEffect(() => {
		submitTerminalInputRef.current = submitTerminalInput;
	}, [submitTerminalInput]);

	useEffect(() => {
		selectedNodeIdRef.current = selectedNodeId;
	}, [selectedNodeId]);

	const onNodesChange: OnNodesChange = useCallback(
		(changes) => {
			setNodes((curr) =>
				applySelectedNode(applyNodeChanges(changes, curr), selectedNodeId),
			);
		},
		[selectedNodeId],
	);

	const onEdgesChange: OnEdgesChange = useCallback((changes) => {
		setEdges((curr) => applyEdgeChanges(changes, curr));
	}, []);

	const createNode = useCallback(async () => {
		setBusy(true);
		setStatus("Creating node...");
		try {
			await createHostNode(baseUrl);
			await loadTopology();
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to create node");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology]);

	const deleteSelectedNode = useCallback(async () => {
		if (!selectedNodeId) {
			setStatus("Select a node first");
			return;
		}

		setBusy(true);
		setStatus(`Deleting node ${selectedNodeId}...`);
		try {
			await deleteNode(baseUrl, selectedNodeId);
			setSelectedNodeId("");
			setNodes((curr) => applySelectedNode(curr, ""));
			await loadTopology();
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to delete node");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology, selectedNodeId]);

	useEffect(() => {
		function onKeyDown(event: KeyboardEvent) {
			if (event.key !== "Delete") {
				return;
			}

			const target = event.target;
			if (
				target instanceof HTMLElement &&
				(target.tagName === "INPUT" ||
					target.tagName === "TEXTAREA" ||
					target.isContentEditable)
			) {
				return;
			}

			event.preventDefault();
			void deleteSelectedNode();
		}

		window.addEventListener("keydown", onKeyDown);
		return () => {
			window.removeEventListener("keydown", onKeyDown);
		};
	}, [deleteSelectedNode]);

	const onConnect: OnConnect = useCallback(
		async (connection: Connection) => {
			const source = connection.source ?? "";
			const target = connection.target ?? "";
			if (!source || !target || source === target) {
				return;
			}

			const key =
				source < target ? `${source}|${target}` : `${target}|${source}`;
			const existing = edgeByPair.get(key);

			setBusy(true);
			try {
				if (existing) {
					setStatus(`Removing link ${existing.id}...`);
					await deleteLink(baseUrl, existing.id);
					await loadTopology();
					setStatus("Link removed");
					return;
				}

				const sourceNode = nodesRef.current.find((node) => node.id === source);
				const targetNode = nodesRef.current.find((node) => node.id === target);
				if (!sourceNode || !targetNode) {
					setStatus("Node not found in canvas");
					return;
				}

				const sourceInterfaces = getAvailableInterfaces(sourceNode.data.interfaces);
				const targetInterfaces = getAvailableInterfaces(targetNode.data.interfaces);
				if (sourceInterfaces.length === 0 || targetInterfaces.length === 0) {
					setStatus("No available interfaces on one of the nodes");
					return;
				}

				setPendingConnection({
					sourceNodeID: source,
					targetNodeID: target,
					sourceInterfaceID: sourceInterfaces[0].id,
					targetInterfaceID: targetInterfaces[0].id,
				});
				setStatus("Choose interfaces for the new link");
			} catch (err: unknown) {
				const message = err instanceof Error ? err.message : String(err);
				setStatus(message || "Failed to update link");
			} finally {
				setBusy(false);
			}
		},
		[baseUrl, edgeByPair, loadTopology],
	);

	const confirmPendingConnection = useCallback(async () => {
		if (!pendingConnection) {
			return;
		}

		setBusy(true);
		setStatus("Creating link...");
		try {
			await createLink(
				baseUrl,
				pendingConnection.sourceInterfaceID,
				pendingConnection.targetInterfaceID,
			);
			setPendingConnection(null);
			await loadTopology();
			setStatus("Link created");
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to create link");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology, pendingConnection]);

	const updatePendingConnection = useCallback(
		(key: "sourceInterfaceID" | "targetInterfaceID", value: string) => {
			setPendingConnection((current) =>
				current
					? {
							...current,
							[key]: value,
						}
					: current,
			);
		},
		[],
	);

	const sourceInterfaceOptions = pendingConnection
		? getAvailableInterfaces(
				nodes.find((node) => node.id === pendingConnection.sourceNodeID)?.data.interfaces ?? [],
			)
		: [];
	const targetInterfaceOptions = pendingConnection
		? getAvailableInterfaces(
				nodes.find((node) => node.id === pendingConnection.targetNodeID)?.data.interfaces ?? [],
			)
		: [];

	return (
		<div className="h-screen w-screen bg-zinc-100 text-zinc-900">
			<header className="fixed left-0 top-0 z-20 flex w-full items-center gap-3 border-b border-zinc-300 bg-white px-4 py-3">
				<button
					type="button"
					onClick={() => void createNode()}
					disabled={busy}
					className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
				>
					Create node
				</button>
				<button
					type="button"
					onClick={() => void deleteSelectedNode()}
					disabled={busy}
					className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
				>
					Delete selected node
				</button>
				<div className="ml-3 text-sm text-zinc-700">
					{selectedNodeId ? `Selected node: ${selectedNodeId}` : "No node selected"}
				</div>
				<div className="ml-3 text-sm text-zinc-600">{status}</div>
			</header>

			<main className="h-full w-full pt-14">
				{pendingConnection ? (
					<div className="fixed left-1/2 top-20 z-30 w-[360px] -translate-x-1/2 rounded border border-zinc-300 bg-white p-4 shadow-lg">
						<div className="mb-3 text-sm font-semibold text-zinc-900">Choose interfaces</div>
						<div className="mb-3 flex flex-col gap-1">
							<label className="text-xs text-zinc-600" htmlFor="source-interface">
								Source interface
							</label>
							<select
								id="source-interface"
								value={pendingConnection.sourceInterfaceID}
								onChange={(event) => updatePendingConnection("sourceInterfaceID", event.target.value)}
								className="rounded border border-zinc-300 px-2 py-1 text-sm"
							>
								{sourceInterfaceOptions.map((iface) => (
									<option key={iface.id} value={iface.id}>
										{iface.name}
									</option>
								))}
							</select>
						</div>
						<div className="mb-4 flex flex-col gap-1">
							<label className="text-xs text-zinc-600" htmlFor="target-interface">
								Target interface
							</label>
							<select
								id="target-interface"
								value={pendingConnection.targetInterfaceID}
								onChange={(event) => updatePendingConnection("targetInterfaceID", event.target.value)}
								className="rounded border border-zinc-300 px-2 py-1 text-sm"
							>
								{targetInterfaceOptions.map((iface) => (
									<option key={iface.id} value={iface.id}>
										{iface.name}
									</option>
								))}
							</select>
						</div>
						<div className="flex justify-end gap-2">
							<button
								type="button"
								onClick={() => setPendingConnection(null)}
								className="rounded border border-zinc-300 px-3 py-1 text-sm hover:bg-zinc-100"
							>
								Cancel
							</button>
							<button
								type="button"
								onClick={() => void confirmPendingConnection()}
								disabled={busy}
								className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
							>
								Create link
							</button>
						</div>
					</div>
				) : null}
				<ReactFlow
					nodes={nodes}
					edges={edges}
					nodeTypes={nodeTypes}
					onNodesChange={onNodesChange}
					onEdgesChange={onEdgesChange}
					onConnect={(connection) => {
						void onConnect(connection);
					}}
					onNodeClick={(_, node) => {
						setSelectedNodeId(node.id);
						setNodes((curr) => applySelectedNode(curr, node.id));
					}}
					onPaneClick={() => {
						setSelectedNodeId("");
						setNodes((curr) => applySelectedNode(curr, ""));
					}}
					zoomOnScroll={false}
					connectionLineType={ConnectionLineType.Straight}
					defaultEdgeOptions={{ type: "straight", style: EDGE_STYLE }}
					nodesConnectable
					fitView
					defaultViewport={{ x: 0, y: 0, zoom: 1 }}
				>
					<MiniMap zoomable pannable nodeStrokeWidth={3} nodeColor="#e5e7eb" />
					<Controls />
					<Background gap={18} size={1} />
				</ReactFlow>
			</main>
		</div>
	);
}
