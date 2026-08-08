// Terminal — landing target of the protected route `/`: renders the terminal control page.
//
// The earlier Home in Root.tsx was a placeholder page after the terminal feature was removed;
// this file wires the real terminal control page back in per the Figma design. Authentication
// is handled by Root's ProtectedRoute guard; here we only mount the component.
import { TerminalPage } from "@/components/terminal/TerminalPage"

export function Terminal() {
  return <TerminalPage />
}