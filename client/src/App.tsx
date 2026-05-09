import { useEffect, useState } from "react";
import { TopologyCanvas } from "./components/TopologyCanvas";
import { HelpPage } from "./pages/HelpPage";

function App() {
	const [pathname, setPathname] = useState(window.location.pathname);

	useEffect(() => {
		const handler = () => setPathname(window.location.pathname);
		window.addEventListener("popstate", handler);
		return () => window.removeEventListener("popstate", handler);
	}, []);

	if (pathname === "/help") {
		return <HelpPage />;
	}
	return <TopologyCanvas />;
}

export default App;
