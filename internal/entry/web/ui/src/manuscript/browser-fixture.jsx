import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { ManuscriptWorkspace } from './ManuscriptWorkspace.jsx';

function BrowserFixture() {
  const [controlsTarget, setControlsTarget] = useState(null);
  return <><aside ref={setControlsTarget} /><ManuscriptWorkspace active controlsTarget={controlsTarget} projectId="browser-project" /></>;
}

createRoot(document.getElementById('root')).render(<BrowserFixture />);
