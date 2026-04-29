import type { NodeProps } from "reactflow";

import HostIcon from "../assets/icons/host";
import RouterIcon from "../assets/icons/router";
import type { ApiInterface } from "../services/topology";
import SwitchIcon from "../assets/icons/switch";
import { NodeTerminal } from "./NodeTerminal";
import { SideHandles } from "./SideHandles";

export type SquareNodeData = {
	nodeId: string;
	type: string;
	status: string;
	interfaces: ApiInterface[];
	isSelected: boolean;
	isBusy: boolean;
	connectionSourceNodeId: string;
	isTerminalOpen: boolean;
	isTerminalFullscreen: boolean;
	terminalInput: string;
	terminalLines: string[];
	terminalHistory: string[];
	terminalHistoryIndex: number | null;
	terminalHistoryDraft: string | null;
	onToggleRun: () => void;
	onToggleTerminal: () => void;
	onToggleTerminalFullscreen: () => void;
	onTerminalInputChange: (value: string) => void;
	onTerminalHistoryNavigate: (direction: "up" | "down") => void;
	onTerminalSubmit: () => void;
};

export function SquareNode({ data }: NodeProps<SquareNodeData>) {
	const isRunning = data.status === "running";
	const Icon = data.type === "router" ? RouterIcon : data.type === "switch" ? SwitchIcon : HostIcon;

	return (
		<div
			className={`relative z-10 flex border border-amber-500 h-[160px] w-[160px] cursor-pointer select-none flex-col items-center justify-start text-center ${data.isTerminalOpen ? "z-[900]" : ""
				}`}
		>
			<div className="relative flex h-[160px] w-[160px] items-center justify-center">
				<button
					type="button"
					onClick={(event) => {
						event.stopPropagation();
						data.onToggleTerminal();
					}}
					className="nodrag nopan absolute left-2 top-2 z-10 flex h-7 w-7 items-center justify-center rounded border border-slate-400 bg-white/95 font-mono text-[11px] font-semibold text-slate-800 hover:bg-slate-100"
					aria-label={data.isTerminalOpen ? "Hide terminal" : "Show terminal"}
				>
					&gt;_
				</button>
				<button
					type="button"
					onClick={(event) => {
						event.stopPropagation();
						void data.onToggleRun();
					}}
					disabled={data.isBusy}
					className="nodrag nopan absolute right-2 top-2 z-10 flex h-7 w-7 items-center justify-center rounded border border-slate-400 bg-white/95 hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50"
					aria-label={isRunning ? "Pause node" : "Run node"}
				>
					{isRunning ? (
						<div className="flex gap-[3px]">
							<span className="block h-3.5 w-1 bg-slate-800" />
							<span className="block h-3.5 w-1 bg-slate-800" />
						</div>
					) : (
						<div className="h-0 w-0 border-b-[7px] border-l-[10px] border-t-[7px] border-b-transparent border-l-slate-800 border-t-transparent" />
					)}
				</button>
				<Icon className="relative z-0 h-full w-full drop-shadow-sm" />
			</div>
			{isRunning && data.isTerminalOpen ? (
				<NodeTerminal
					terminalLines={data.terminalLines}
					terminalInput={data.terminalInput}
					isFullscreen={data.isTerminalFullscreen}
					onInputChange={data.onTerminalInputChange}
					onHistoryNavigate={data.onTerminalHistoryNavigate}
					onSubmit={data.onTerminalSubmit}
					onToggleFullscreen={data.onToggleTerminalFullscreen}
				/>
			) : null}
			<SideHandles currentNodeId={data.nodeId} connectionSourceNodeId={data.connectionSourceNodeId} />
		</div>
	);
}
