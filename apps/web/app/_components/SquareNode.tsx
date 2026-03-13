import type { NodeProps } from "reactflow";

import { SideHandles } from "./SideHandles";

export type SquareNodeData = {
	label: string;
	image: string;
	containerId: string;
	isSelected: boolean;
};

export function SquareNode({ data }: NodeProps<SquareNodeData>) {
	return (
		<div
			className={
				data.isSelected
					? "relative flex h-[160px] w-[160px] cursor-pointer select-none items-center justify-center border-2 border-blue-600 bg-blue-50 p-3 text-center shadow-sm shadow-blue-500/20 ring-4 ring-blue-500/20"
					: "relative flex h-[160px] w-[160px] cursor-pointer select-none items-center justify-center border-2 border-slate-800 bg-white p-3 text-center shadow-sm"
			}
		>
			<div className="pointer-events-none flex flex-col gap-2">
				<div className="text-[13px] font-semibold leading-tight text-zinc-900">{data.label}</div>
				<div className="text-[11px] leading-tight text-zinc-500">{data.image}</div>
			</div>
			<SideHandles />
		</div>
	);
}
