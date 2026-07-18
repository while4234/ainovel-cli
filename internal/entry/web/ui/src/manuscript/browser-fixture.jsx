import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { ManuscriptWorkspace } from './ManuscriptWorkspace.jsx';

function BrowserFixture() {
  const [discussion, setDiscussion] = useState('');
  const [controlsTarget, setControlsTarget] = useState(null);
  return <><aside ref={setControlsTarget} /><ManuscriptWorkspace active controlsTarget={controlsTarget} projectId="browser-project" onDiscussionReady={(message) => setDiscussion(message)} />{discussion ? <pre aria-label="共创已接收上下文">{discussion}</pre> : null}</>;
}

createRoot(document.getElementById('root')).render(<BrowserFixture />);
