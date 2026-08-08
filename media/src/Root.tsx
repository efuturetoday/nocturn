import React from "react";
import { Composition } from "remotion";
import { DURATION, FPS } from "./anim";
import { Mockup } from "./mockup/Mockup";

export const RemotionRoot: React.FC = () => (
  <Composition
    id="mockup"
    component={Mockup}
    durationInFrames={DURATION}
    fps={FPS}
    width={1179}
    height={2556}
  />
);
