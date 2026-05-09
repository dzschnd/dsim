import {
	AlertCircle,
	CheckCircle,
	ChevronDown,
	Snowflake,
	Loader2,
	Monitor,
	Network,
	PencilLine,
	Play,
	Router as RouterIcon,
	Square,
	Trash2,
	X,
	Terminal,
} from "lucide-react";
import { useEffect, useState } from "react";
import type { Edge, Node } from "reactflow";
import type { InterfaceLabelEdgeData } from "./InterfaceLabelEdge";
import type { SquareNodeData } from "./SquareNode";

export type SidebarLastCommand = {
	command: string;
	status: "idle" | "executing" | "done" | "success" | "failed";
};

type SidebarProps = {
	selectedNode: Node<SquareNodeData> | null;
	selectedEdge: Edge<InterfaceLabelEdgeData> | null;
	nodes: Node<SquareNodeData>[];
	edges?: Edge<InterfaceLabelEdgeData>[];
	isBusy: boolean;
	isCollapsed: boolean;
	recentCommands: string[];
	lastCommand: SidebarLastCommand | null;
	onRenameNode: (nodeId: string, displayName: string) => void;
	onOpenTerminal: (nodeId: string) => void;
	onToggleRun: (nodeId: string) => void;
	onToggleFreeze: (nodeId: string) => void;
	onRequestDeleteNode: () => void;
	onDeleteLink: () => void;
	onToggleCollapse: () => void;
};

function NodeIcon({ type, className }: { type: string; className?: string }) {
	const cls = className ?? "w-5 h-5 text-gray-600";
	if (type === "router") return <RouterIcon className={cls} />;
	if (type === "switch") return <Network className={cls} />;
	return <Monitor className={cls} />;
}

const TYPE_LABEL: Record<string, string> = {
	host: "Endpoint device",
	switch: "Network switch",
	router: "Network router",
};

function getInterfaceStateLabel(state: "connected" | "disconnected") {
	return state === "connected" ? "Connected" : "Disconnected";
}

function getInterfaceStateColor(state: "connected" | "disconnected") {
	return state === "connected" ? "text-green-600" : "text-gray-400";
}

function NodePanel({
	node,
	isBusy,
	recentCommands,
	lastCommand,
	onOpenTerminal,
	onToggleRun,
	onToggleFreeze,
	onRequestDeleteNode,
	onCollapse,
	onRenameNode,
}: {
	node: Node<SquareNodeData>;
	isBusy: boolean;
	recentCommands: string[];
	lastCommand: SidebarLastCommand | null;
	onOpenTerminal: (nodeId: string) => void;
	onToggleRun: (nodeId: string) => void;
	onToggleFreeze: (nodeId: string) => void;
	onRequestDeleteNode: () => void;
	onCollapse: () => void;
	onRenameNode: (nodeId: string, displayName: string) => void;
}) {
	const { data } = node;
	const nodeBusy = data.isBusy;
	const isRunning = data.status === "running" || data.status === "frozen";
	const isFrozen = data.status === "frozen";
	const [interfacesCollapsed, setInterfacesCollapsed] = useState(false);
	const [editingName, setEditingName] = useState(false);
	const [draftName, setDraftName] = useState(data.displayName);
	useEffect(() => setDraftName(data.displayName), [data.displayName]);

	const statusDot = nodeBusy ? "bg-blue-400" : isFrozen ? "bg-sky-300" : isRunning ? "bg-green-500" : "bg-gray-400";
	const statusLabel = nodeBusy ? "Loading" : isFrozen ? "Frozen" : isRunning ? "On" : "Off";

	return (
		<div className="flex h-full flex-col overflow-hidden">
			{/* Header */}
			<div className="p-4 border-b border-gray-200 flex items-start justify-between flex-shrink-0">
				<div className="flex items-center gap-3">
					<NodeIcon type={data.type} />
					<div>
						<div className="relative h-8 w-[220px]">
							{editingName ? (
								<input
									value={draftName}
									onChange={(e) => setDraftName(e.target.value)}
									onBlur={() => {
										onRenameNode(data.nodeId, draftName);
										setEditingName(false);
									}}
									onKeyDown={(e) => {
										if (e.key === "Enter") {
											onRenameNode(data.nodeId, draftName);
											setEditingName(false);
										}
									}}
									className="absolute inset-0 h-8 w-[220px] rounded-md border border-blue-200 bg-blue-50 px-2.5 text-sm font-semibold text-gray-900 shadow-sm outline-none focus:border-blue-400 focus:bg-white"
									autoFocus
								/>
							) : (
								<button type="button" onClick={() => setEditingName(true)} className="absolute inset-0 flex h-8 w-[220px] items-center gap-1.5 text-left text-base font-semibold text-gray-900">
									<span className="min-w-0 flex-1 truncate">{data.displayName}</span>
									<PencilLine className="h-3.5 w-3.5 shrink-0 text-gray-400" />
								</button>
							)}
						</div>
						<p className="text-sm text-gray-500">{TYPE_LABEL[data.type] ?? data.type}</p>
					</div>
				</div>
				<button
					type="button"
					onClick={onCollapse}
					className="w-7 h-7 flex items-center justify-center rounded-md hover:bg-gray-100 transition-colors flex-shrink-0"
				>
					<X className="w-4 h-4 text-gray-500" />
				</button>
			</div>

			{/* Content */}
					<div className="flex-1 overflow-y-auto p-5 space-y-5">
				{/* Status */}
				<div>
					<h3 className="text-sm font-medium text-gray-700 mb-2">Status</h3>
					<div className="flex items-center gap-2">
						<div className={`w-2 h-2 rounded-full ${statusDot}`} />
						<span className="text-sm text-gray-900">{statusLabel}</span>
					</div>
				</div>

				{/* Interfaces */}
				<div>
					<button
						type="button"
						onClick={() => setInterfacesCollapsed((v) => !v)}
						className="mb-2 flex items-center gap-1 text-sm font-medium text-gray-700"
					>
						Interfaces
						<ChevronDown className={`h-4 w-4 transition-transform ${interfacesCollapsed ? "-rotate-90" : ""}`} />
					</button>
					<div
						className={`overflow-hidden transition-all duration-200 ${interfacesCollapsed ? "max-h-0 -translate-y-2 opacity-0" : "max-h-[420px] translate-y-0 opacity-100"}`}
					>
						<div className="space-y-2">
						{data.interfaces.map((iface) => {
							const isLinked = iface.linkId !== "";
							const state = isLinked ? "connected" as const : "disconnected" as const;
							const hasIp = iface.ipAddress !== "" && iface.prefixLength > 0;
							return (
								<div key={iface.id} className="p-3 bg-gray-50 rounded-lg border border-gray-200">
									<div className="flex items-center justify-between mb-1">
										<div className="text-sm font-medium text-gray-900">{iface.name}</div>
										<span className={`text-xs ${getInterfaceStateColor(state)}`}>
											{getInterfaceStateLabel(state)}
										</span>
									</div>
									{hasIp && (
										<div className="text-sm text-gray-600">
											{iface.ipAddress}/{iface.prefixLength}
										</div>
									)}
								</div>
							);
						})}
						{data.interfaces.length === 0 && (
							<div className="text-sm text-gray-400">No interfaces</div>
						)}
						</div>
					</div>
				</div>

				{/* Last command */}
				{lastCommand && (
					<div>
						<h3 className="text-sm font-medium text-gray-700 mb-2">Last command</h3>
						<div className="flex items-center gap-2">
							{lastCommand.status === "executing" ? (
								<Loader2 className="w-3.5 h-3.5 text-blue-500 animate-spin" />
							) : lastCommand.status === "success" || lastCommand.status === "done" ? (
								<CheckCircle className="w-3.5 h-3.5 text-green-500" />
							) : lastCommand.status === "failed" ? (
								<AlertCircle className="w-3.5 h-3.5 text-red-500" />
							) : (
								<div className="w-2 h-2 rounded-full bg-gray-400" />
							)}
							<span className="text-sm text-gray-900 capitalize">{lastCommand.status}</span>
							<span className="text-sm text-gray-500 truncate">— {lastCommand.command}</span>
						</div>
					</div>
				)}

				{/* Recent commands */}
				{recentCommands.length > 0 && (
					<div>
						<h3 className="text-sm font-medium text-gray-700 mb-2">Recent commands</h3>
						<div className="space-y-1">
							{recentCommands.slice(-3).reverse().map((cmd, idx) => (
								<div key={idx} className="text-xs font-mono text-gray-600 px-2 py-1 bg-gray-50 rounded">
									{cmd}
								</div>
							))}
						</div>
					</div>
				)}

				{/* Actions */}
				<div>
					<h3 className="text-sm font-medium text-gray-700 mb-3">Actions</h3>
					<div className="space-y-2">
						<button
							type="button"
							onClick={() => onOpenTerminal(data.nodeId)}
							className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-gray-200 bg-white hover:bg-gray-50 transition-colors text-sm"
						>
							<Terminal className="w-4 h-4" />
							Terminal
						</button>
						<button
							type="button"
							onClick={() => onToggleRun(data.nodeId)}
							disabled={nodeBusy}
							className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-gray-200 bg-white hover:bg-gray-50 transition-colors text-sm disabled:opacity-50 disabled:cursor-default"
						>
							{isRunning ? (
								<><Square className="w-4 h-4" />Stop</>
							) : (
								<><Play className="w-4 h-4" />Start</>
							)}
						</button>
						{isRunning && (
							<button
								type="button"
								onClick={() => onToggleFreeze(data.nodeId)}
								disabled={nodeBusy}
								className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-gray-200 bg-white hover:bg-gray-50 transition-colors text-sm disabled:opacity-50 disabled:cursor-default"
							>
								<Snowflake className="w-4 h-4 text-sky-400" />
								{isFrozen ? "Unfreeze" : "Freeze"}
							</button>
						)}
						<button
							type="button"
							onClick={onRequestDeleteNode}
							disabled={nodeBusy}
							className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-red-200 bg-white hover:bg-red-50 hover:border-red-300 transition-colors text-sm text-red-600 disabled:opacity-50 disabled:cursor-default"
						>
							<Trash2 className="w-4 h-4" />
							Delete
						</button>
					</div>
				</div>
			</div>
		</div>
	);
}

function EdgePanel({
	edge,
	nodes,
	isBusy,
	onDeleteLink,
	onCollapse,
}: {
	edge: Edge<InterfaceLabelEdgeData>;
	nodes: Node<SquareNodeData>[];
	isBusy: boolean;
	onDeleteLink: () => void;
	onCollapse: () => void;
}) {
	const sourceNode = nodes.find((n) => n.id === edge.source);
	const targetNode = nodes.find((n) => n.id === edge.target);
	const { data } = edge;

	return (
		<div className="flex h-full flex-col overflow-hidden">
			{/* Header */}
			<div className="p-4 border-b border-gray-200 flex items-start justify-between flex-shrink-0">
				<div>
					<h2 className="text-base font-semibold text-gray-900">Link Details</h2>
				</div>
				<button
					type="button"
					onClick={onCollapse}
					className="w-7 h-7 flex items-center justify-center rounded-md hover:bg-gray-100 transition-colors flex-shrink-0"
				>
					<X className="w-4 h-4 text-gray-500" />
				</button>
			</div>

			{/* Content */}
			<div className="flex-1 overflow-y-auto p-5 space-y-5">
				<div className="p-3 bg-gray-50 rounded-lg border border-gray-200">
					<div className="flex items-center gap-2 mb-1">
						{sourceNode && <NodeIcon type={sourceNode.data.type} className="w-4 h-4 text-gray-500" />}
						<div className="max-w-[180px] truncate text-sm font-medium text-gray-900">{sourceNode?.data.displayName ?? edge.source}</div>
					</div>
					<div className="text-xs text-gray-500">{data?.interfaceAName}</div>
					{data?.interfaceAIP && <div className="text-xs text-gray-500 mt-0.5">{data.interfaceAIP}</div>}
				</div>

				<div className="p-3 bg-gray-50 rounded-lg border border-gray-200">
					<div className="flex items-center gap-2 mb-1">
						{targetNode && <NodeIcon type={targetNode.data.type} className="w-4 h-4 text-gray-500" />}
						<div className="max-w-[180px] truncate text-sm font-medium text-gray-900">{targetNode?.data.displayName ?? edge.target}</div>
					</div>
					<div className="text-xs text-gray-500">{data?.interfaceBName}</div>
					{data?.interfaceBIP && <div className="text-xs text-gray-500 mt-0.5">{data.interfaceBIP}</div>}
				</div>

				<div>
					<h3 className="text-sm font-medium text-gray-700 mb-3">Actions</h3>
					<button
						type="button"
						onClick={onDeleteLink}
						disabled={isBusy}
						className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-red-200 bg-white hover:bg-red-50 hover:border-red-300 transition-colors text-sm text-red-600 disabled:opacity-50 disabled:cursor-default"
					>
						<Trash2 className="w-4 h-4" />
						Unlink
					</button>
				</div>
			</div>
		</div>
	);
}

export function Sidebar({
	selectedNode,
	selectedEdge,
	nodes,
	edges: _edges,
	isBusy,
	isCollapsed,
	recentCommands,
	lastCommand,
	onRenameNode,
	onOpenTerminal,
	onToggleRun,
	onToggleFreeze,
	onRequestDeleteNode,
	onDeleteLink,
	onToggleCollapse,
}: SidebarProps) {
	const hasSelection = selectedNode !== null || selectedEdge !== null;
	const hidden = !hasSelection && isCollapsed;

	if (hidden) return null;

	return (
		<aside
			className={`fixed right-0 top-14 z-[700] flex flex-col border-l border-gray-200 bg-white overflow-hidden transition-all duration-200 ${
				isCollapsed ? "w-0 opacity-0" : "w-[320px] opacity-100"
			}`}
			style={{
				height: "calc(100vh - 56px)",
			}}
		>
			{selectedNode ? (
				<NodePanel
					node={selectedNode}
					isBusy={isBusy}
					recentCommands={recentCommands}
					lastCommand={lastCommand}
					onOpenTerminal={onOpenTerminal}
					onToggleRun={onToggleRun}
					onToggleFreeze={onToggleFreeze}
					onRequestDeleteNode={onRequestDeleteNode}
					onCollapse={onToggleCollapse}
					onRenameNode={onRenameNode}
				/>
			) : selectedEdge ? (
				<EdgePanel
					edge={selectedEdge}
					nodes={nodes}
					isBusy={isBusy}
					onDeleteLink={onDeleteLink}
					onCollapse={onToggleCollapse}
				/>
			) : null}
		</aside>
	);
}
