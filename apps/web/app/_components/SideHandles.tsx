import { Handle, Position } from "reactflow";

export function SideHandles() {
	const sharedClass =
		"!left-1/2 !top-1/2 !h-[16px] !w-[16px] !-translate-x-1/2 !-translate-y-1/2 !rounded-full !border-2 !border-slate-700/50 !bg-white/35";

	return (
		<>
			<Handle id="center-target" type="target" position={Position.Top} className={sharedClass} />
			<Handle id="center-source" type="source" position={Position.Top} className={sharedClass} />
		</>
	);
}
